-- The message log is the table that grows without bound: every message in
-- and out, bytes included. Dropping a month of history should be dropping a
-- partition, not deleting a hundred million rows through the WAL.
--
-- A table cannot be converted to partitioned in place. When the log is empty
-- -- a fresh deployment, which is where this migration is expected to run --
-- it is rebuilt partitioned by time, with a DEFAULT partition catching
-- everything until an operator cuts monthly partitions. When the log already
-- holds data it is left exactly as it is, with a notice: a migration must
-- never quietly rewrite somebody's evidence. Adopting partitioning on a
-- populated log is a manual operation done with eyes open.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM message LIMIT 1) THEN
        RAISE NOTICE 'message log holds data; partitioning skipped -- adopt manually if wanted';
        RETURN;
    END IF;
    IF (SELECT relkind FROM pg_class WHERE relname = 'message'
        AND relnamespace = current_schema()::regnamespace) = 'p' THEN
        RETURN; -- already partitioned
    END IF;

    DROP TABLE message;

    CREATE TABLE message (
        id              text NOT NULL,
        direction       text NOT NULL CHECK (direction IN ('in','out')),
        at              timestamptz NOT NULL,
        transport       text NOT NULL,
        peer            text NOT NULL,
        format          text NOT NULL,
        kind            text,
        raw             bytea NOT NULL,
        sha256          text NOT NULL,
        size_bytes      integer NOT NULL,
        status          text NOT NULL,
        error           text,
        dedup_key       text,
        pnr_id          text,
        correlation_id  text,
        diagnostics     jsonb NOT NULL DEFAULT '[]'::jsonb,
        possible_duplicate boolean NOT NULL DEFAULT false,
        trace_id        text,
        span_id         text,
        -- The partition key must be part of the key; the ULID id remains
        -- unique in practice because it embeds its own timestamp.
        PRIMARY KEY (id, at)
    ) PARTITION BY RANGE (at);

    CREATE TABLE message_default PARTITION OF message DEFAULT;

    CREATE INDEX message_at_idx ON message (at DESC);
    CREATE INDEX message_peer_idx ON message (peer, id DESC);
    CREATE INDEX message_pnr_idx ON message (pnr_id, id) WHERE pnr_id IS NOT NULL;
    CREATE INDEX message_status_idx ON message (status, id DESC);
    CREATE INDEX message_correlation_idx ON message (correlation_id) WHERE correlation_id IS NOT NULL;
    CREATE INDEX message_dedup_idx ON message (peer, dedup_key) WHERE dedup_key IS NOT NULL;
END $$;
