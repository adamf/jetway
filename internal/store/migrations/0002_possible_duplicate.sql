-- Records the Type B PDM (possible duplicate message) indicator.
--
-- Inbound it is what the sender asserted; outbound it is what we stamped on a
-- redelivery. Keeping it lets an operator tell a retransmission the network
-- expected from a repeat that means the sender lost track of its own state.
ALTER TABLE message ADD COLUMN IF NOT EXISTS possible_duplicate boolean NOT NULL DEFAULT false;
