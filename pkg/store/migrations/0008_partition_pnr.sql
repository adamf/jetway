-- Records are retired, not deleted. A booking is live until its last flight
-- has flown and a few days have passed; then it goes to the archive and out
-- of the book of record, every day, in bulk. Deleting a day's million rows
-- through the WAL is minutes of I/O and a table full of dead tuples; dropping
-- the day's partition is a metadata change.
--
-- purge_at is when a record may be retired: the departure of its last air
-- segment plus the deployment's grace, or a year after creation when it
-- holds no flight. Records and their events are partitioned by it, one
-- partition per day, created on demand by the store, and Postgres.RetireBefore
-- drops every partition whose day has passed. Unlike the message log, a
-- populated book is converted here: the copy is done inside the migration's
-- transaction and the old table is dropped only once the new one holds every
-- row.
DO $$
BEGIN
    IF (SELECT relkind FROM pg_class WHERE relname = 'pnr'
        AND relnamespace = current_schema()::regnamespace) = 'p' THEN
        RETURN; -- already partitioned
    END IF;

    ALTER TABLE pnr ADD COLUMN IF NOT EXISTS purge_at timestamptz;
    UPDATE pnr SET purge_at = coalesce(
        (SELECT max((seg->>'depart')::timestamptz)
           FROM jsonb_array_elements(CASE WHEN jsonb_typeof(state->'segments') = 'array' THEN state->'segments' ELSE '[]'::jsonb END) seg
          WHERE seg->>'type' = 'air' AND (seg->>'depart') IS NOT NULL AND seg->>'depart' <> '0001-01-01T00:00:00Z'),
        created_at + interval '365 days')
    WHERE purge_at IS NULL;

    ALTER TABLE pnr_event ADD COLUMN IF NOT EXISTS purge_at timestamptz;
    UPDATE pnr_event e SET purge_at = p.purge_at FROM pnr p WHERE p.id = e.pnr_id AND e.purge_at IS NULL;
    UPDATE pnr_event SET purge_at = at + interval '365 days' WHERE purge_at IS NULL;

    -- A foreign key cannot point at a partitioned table by id alone; the
    -- events and queue items are retired with their records instead.
    ALTER TABLE pnr_event DROP CONSTRAINT IF EXISTS pnr_event_pnr_id_fkey;
    ALTER TABLE queue_item DROP CONSTRAINT IF EXISTS queue_item_pnr_id_fkey;

    CREATE TABLE pnr_new (LIKE pnr INCLUDING DEFAULTS) PARTITION BY RANGE (purge_at);
    ALTER TABLE pnr_new ALTER COLUMN purge_at SET NOT NULL;
    ALTER TABLE pnr_new ADD PRIMARY KEY (id, purge_at);
    CREATE TABLE pnr_new_default PARTITION OF pnr_new DEFAULT;
    INSERT INTO pnr_new SELECT * FROM pnr;
    DROP TABLE pnr;
    ALTER TABLE pnr_new RENAME TO pnr;
    ALTER TABLE pnr_new_default RENAME TO pnr_default;
    -- A locator is unique within a system for the life of the record; the
    -- partition key has to ride along, so the same six characters may be
    -- issued again once the earlier record has been retired.
    CREATE UNIQUE INDEX pnr_node_locator_idx ON pnr (node, record_locator, purge_at);
    CREATE INDEX pnr_id_idx ON pnr (id);
    CREATE INDEX pnr_updated_idx ON pnr (updated_at DESC);
    CREATE INDEX pnr_status_idx ON pnr (status, updated_at DESC);
    CREATE INDEX pnr_state_idx ON pnr USING gin (state jsonb_path_ops);
    CREATE INDEX pnr_deadline_idx ON pnr (next_deadline) WHERE next_deadline IS NOT NULL;
    CREATE INDEX pnr_stale_idx ON pnr (updated_at) WHERE status <> 'cancelled';
    CREATE INDEX pnr_node_updated_idx ON pnr (node, updated_at DESC);

    CREATE TABLE pnr_event_new (LIKE pnr_event INCLUDING DEFAULTS) PARTITION BY RANGE (purge_at);
    ALTER TABLE pnr_event_new ALTER COLUMN purge_at SET NOT NULL;
    ALTER TABLE pnr_event_new ADD PRIMARY KEY (id, purge_at);
    CREATE TABLE pnr_event_new_default PARTITION OF pnr_event_new DEFAULT;
    INSERT INTO pnr_event_new SELECT * FROM pnr_event;
    DROP TABLE pnr_event;
    ALTER TABLE pnr_event_new RENAME TO pnr_event;
    ALTER TABLE pnr_event_new_default RENAME TO pnr_event_default;
    CREATE UNIQUE INDEX pnr_event_pnr_seq_idx ON pnr_event (pnr_id, seq, purge_at);
    CREATE INDEX pnr_event_pnr_idx ON pnr_event (pnr_id, seq);
    CREATE INDEX pnr_event_message_idx ON pnr_event (message_id) WHERE message_id IS NOT NULL;
    CREATE INDEX pnr_event_node_at_idx ON pnr_event (node, at);
END $$;
