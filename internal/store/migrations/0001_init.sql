-- Jetway schema, revision 1.
--
-- Two ideas shape this schema.
--
-- The message table is an append-only log of exact wire bytes. It is written
-- before anything is interpreted and is never rewritten, only transitioned
-- through statuses. That is what makes reprocessing after a parser fix possible
-- and what makes "show me what the partner actually sent" answerable.
--
-- PNR state is a projection. pnr.state holds the current record for fast reads,
-- and pnr_event holds every change with the id of the message that caused it.
-- Reads stay cheap; provenance stays complete.

CREATE TABLE IF NOT EXISTS message (
    id              text PRIMARY KEY,           -- ULID: sorts in receive order
    direction       text NOT NULL CHECK (direction IN ('in','out')),
    at              timestamptz NOT NULL,
    transport       text NOT NULL,
    peer            text NOT NULL,
    format          text NOT NULL,
    kind            text,

    -- The bytes exactly as they crossed the wire. Never regenerated from a
    -- parse: a re-encode is a different artefact and must not be mistaken for
    -- evidence of what a partner sent.
    raw             bytea NOT NULL,
    sha256          text NOT NULL,
    size_bytes      integer NOT NULL,

    status          text NOT NULL,
    error           text,

    -- Application-level idempotency key, where the message class has one.
    dedup_key       text,

    pnr_id          text,
    correlation_id  text,
    diagnostics     jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS message_at_idx ON message (at DESC);
CREATE INDEX IF NOT EXISTS message_peer_idx ON message (peer, id DESC);
CREATE INDEX IF NOT EXISTS message_pnr_idx ON message (pnr_id, id) WHERE pnr_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS message_status_idx ON message (status, id DESC);
CREATE INDEX IF NOT EXISTS message_correlation_idx ON message (correlation_id) WHERE correlation_id IS NOT NULL;

-- Deliberately not unique. A retransmission is a real event that must be
-- recorded; what must not happen is applying it twice. Detection is a lookup,
-- not a constraint violation.
CREATE INDEX IF NOT EXISTS message_dedup_idx ON message (peer, dedup_key) WHERE dedup_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS pnr (
    id              text PRIMARY KEY,
    record_locator  text NOT NULL UNIQUE,

    -- Optimistic concurrency token. A gateway and a carrier can be changing one
    -- record at the same moment; a write that does not check this silently
    -- discards whichever change it did not see.
    version         bigint NOT NULL,

    status          text NOT NULL,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    state           jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS pnr_updated_idx ON pnr (updated_at DESC);
CREATE INDEX IF NOT EXISTS pnr_status_idx ON pnr (status, updated_at DESC);
CREATE INDEX IF NOT EXISTS pnr_state_idx ON pnr USING gin (state jsonb_path_ops);

CREATE TABLE IF NOT EXISTS pnr_event (
    id          text PRIMARY KEY,
    pnr_id      text NOT NULL REFERENCES pnr(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    type        text NOT NULL,
    detail      text,
    payload     jsonb,

    -- Which wire message caused this change. Null only for changes originating
    -- inside the gateway, such as a timeout sweep.
    message_id  text,
    actor       text,
    at          timestamptz NOT NULL,

    UNIQUE (pnr_id, seq)
);

CREATE INDEX IF NOT EXISTS pnr_event_message_idx ON pnr_event (message_id) WHERE message_id IS NOT NULL;

-- Backs record locator allocation. The sequence supplies uniqueness; the
-- Feistel permutation in pkg/pnr supplies unpredictability. Neither alone is
-- sufficient: a bare sequence leaks booking volume, and bare randomness needs a
-- retry loop that contends under load.
CREATE SEQUENCE IF NOT EXISTS locator_counter AS bigint START 1;
