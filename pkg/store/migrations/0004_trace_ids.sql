-- Trace identifiers for a message.
--
-- A trace is sampled and eventually expires; the message log does not. Keeping
-- the identifiers here means a message can still name the trace that handled
-- it long after the spans have gone, which is the difference between "this one
-- failed" and "here is what was happening around it".
ALTER TABLE message ADD COLUMN IF NOT EXISTS trace_id text;
ALTER TABLE message ADD COLUMN IF NOT EXISTS span_id  text;

CREATE INDEX IF NOT EXISTS message_trace_idx ON message (trace_id) WHERE trace_id IS NOT NULL;
