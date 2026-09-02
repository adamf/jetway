-- One writer per system. A system's links may be held by exactly one
-- process at a time: two processes answering one partner is two answers to
-- one sell. The lease is a row per system naming its holder and when the
-- hold expires; a standby takes it when it has lapsed, and the holder
-- renews it well inside the term. It lives in the same database as the
-- book because that is the failure domain that matters: if the book is
-- gone the lease is moot.
CREATE TABLE IF NOT EXISTS system_lease (
    system     text PRIMARY KEY,
    holder     text NOT NULL,
    expires_at timestamptz NOT NULL,
    taken_at   timestamptz NOT NULL DEFAULT now()
);
