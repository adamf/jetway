-- The sweeper used to read the 500 most recently updated records and look for
-- stale ones among them. That is inverted: the freshest records are by
-- definition not the stale ones, so above a few hundred records a ticketing
-- time limit could never fire and an unanswered segment could never be raised.
-- No error, no log line -- the pass simply reported nothing to do.
--
-- Fixing it needs the due-date predicates in the query, and a deadline buried
-- in the JSONB state cannot be range-scanned. So the soonest unmet deadline is
-- lifted into a column and indexed.

ALTER TABLE pnr ADD COLUMN IF NOT EXISTS next_deadline timestamptz;

-- Partial: most records owe no deadline, and the ones that do are the whole
-- query. Ascending because the sweeper wants the most overdue first, so a
-- limit truncates the least urgent rather than the most.
CREATE INDEX IF NOT EXISTS pnr_deadline_idx ON pnr (next_deadline)
    WHERE next_deadline IS NOT NULL;

-- Staleness is asked the same way, oldest first, for the same reason.
CREATE INDEX IF NOT EXISTS pnr_stale_idx ON pnr (updated_at)
    WHERE status <> 'cancelled';

-- Backfill. Records written before this column existed still owe their
-- deadlines, and leaving them null would keep exactly the bug being fixed.
UPDATE pnr SET next_deadline = (
    SELECT min((t->>'deadline')::timestamptz)
    FROM jsonb_array_elements(state->'ticketing') AS t
    WHERE t->>'deadline' IS NOT NULL
)
WHERE next_deadline IS NULL
  AND status <> 'cancelled'
  AND jsonb_typeof(state->'ticketing') = 'array';
