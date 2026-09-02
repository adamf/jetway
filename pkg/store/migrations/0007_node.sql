-- One database, many systems. A hosted reservations platform runs hundreds
-- of carriers' systems on one book of record, and each must see only its
-- own. The node column is which system a row belongs to; the empty string
-- is the single-tenant deployment every earlier row was written by, so
-- nothing existing changes meaning.
ALTER TABLE message    ADD COLUMN IF NOT EXISTS node text NOT NULL DEFAULT '';
ALTER TABLE pnr        ADD COLUMN IF NOT EXISTS node text NOT NULL DEFAULT '';
ALTER TABLE pnr_event  ADD COLUMN IF NOT EXISTS node text NOT NULL DEFAULT '';
ALTER TABLE queue_item ADD COLUMN IF NOT EXISTS node text NOT NULL DEFAULT '';

-- A record locator is unique within a system, not across the database:
-- two carriers can, and do, issue the same six characters.
ALTER TABLE pnr DROP CONSTRAINT IF EXISTS pnr_record_locator_key;
CREATE UNIQUE INDEX IF NOT EXISTS pnr_node_locator_idx ON pnr (node, record_locator);

CREATE INDEX IF NOT EXISTS pnr_node_updated_idx ON pnr (node, updated_at DESC);
CREATE INDEX IF NOT EXISTS pnr_event_node_at_idx ON pnr_event (node, at);
CREATE INDEX IF NOT EXISTS message_node_idx ON message (node, id DESC);
CREATE INDEX IF NOT EXISTS queue_node_idx ON queue_item (node, id DESC) WHERE worked_at IS NULL;
