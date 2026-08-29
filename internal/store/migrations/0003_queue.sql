-- Work queues.
--
-- A queue item is a record waiting for action, with the reason it is waiting
-- and the message that put it there. Items are not deleted when worked: the
-- row records who cleared it and when, which is the question asked after an
-- interline dispute.
CREATE TABLE IF NOT EXISTS queue_item (
    id           text PRIMARY KEY,
    queue        text NOT NULL,
    pnr_id       text NOT NULL REFERENCES pnr (id) ON DELETE CASCADE,
    locator      text,

    -- Stable machine-readable reason, and the idempotency key below.
    code         text NOT NULL,
    reason       text NOT NULL,

    message_id   text,
    segment_ref  integer NOT NULL DEFAULT 0,

    placed_at    timestamptz NOT NULL,
    placed_by    text NOT NULL,

    worked_at    timestamptz,
    worked_by    text,
    note         text
);

-- A segment sits on a given queue at most once per reason while it is pending.
-- This is what lets a sweeper run every minute without stacking up duplicates,
-- and it is a constraint rather than a lookup because two sweepers racing must
-- not both succeed.
--
-- segment_ref is part of the key because an interline record has one segment
-- per carrier and each carrier answers separately: two partners confirming the
-- same booking are two pieces of work, not one. Record-level placements such as
-- a ticketing time limit use segment_ref 0 and so still collapse to one.
CREATE UNIQUE INDEX IF NOT EXISTS queue_pending_idx
    ON queue_item (queue, pnr_id, code, segment_ref) WHERE worked_at IS NULL;

CREATE INDEX IF NOT EXISTS queue_listing_idx ON queue_item (queue, id DESC) WHERE worked_at IS NULL;
CREATE INDEX IF NOT EXISTS queue_pnr_idx ON queue_item (pnr_id, id DESC);
