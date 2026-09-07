# Architecture

## The inbound pipeline

Every inbound message goes through the same stages in the same order. This
order is the contract. `pkg/gateway.Ingest` enforces it.

```
bytes from a partner
      │
      ▼
┌─────────────┐  from the certificate presented, the network connected from, or
│ 0. identify │  a listener dedicated to one partner. Never from the payload.
└─────────────┘  An unmapped certificate is refused, not defaulted.
      │
      ▼
┌─────────────┐  the raw bytes and their digest are written and durable before
│ 1. capture  │  anything reads them -- to the spool when one is configured, so
└─────────────┘  acknowledging does not depend on the database being up.
      │
      ▼
┌─────────────┐  by content, not by link configuration. A link configured as
│ 2. classify │  teletype that starts carrying EDIFACT gets processed, and
└─────────────┘  noticed, rather than mangled by the wrong decoder.
      │
      ▼
┌─────────────┐  syntax first (envelope, segments), then the message grammar
│ 3. decode   │  for the link's profile. Diagnostics are collected, never
└─────────────┘  thrown. Anything unrecognised is preserved verbatim.
      │
      ▼
┌─────────────┐  by the sender's own reference where one exists — an EDIFACT
│ 4. dedupe   │  interchange control reference — and by originator, time group
└─────────────┘  and digest for teletype, which carries no such field. A Type B
      │          sender that marks a resend PDM is recorded as a retransmission
      │          rather than a divergence.
      ▼
┌─────────────┐  resolve the record by locator, fold the message into it, write
│ 5. apply    │  the new state with the version it was read at. On conflict,
└─────────────┘  re-read and reapply.
      │
      ▼
┌─────────────┐  a segment status the partner just changed into something a
│ 6. queue    │  person must act on becomes a queue item. Driven by the
└─────────────┘  transition, not the resulting state, so a later message
      │          touching a settled record does not re-raise it.
      ▼
┌─────────────┐  only for message classes that expect an answer. The decision is
│ 7. respond  │  recorded on our own record before the answer is sent, so our
└─────────────┘  state and our answer can never disagree.
```

A failure at any stage after capture leaves the message in the dead letter
queue. The queue keeps the bytes intact and records the reason. The pipeline
never drops the message, and it never records a failure as a success.

## Capture before decoding

The alternative is to decode a message and then store the decoded result.
This loses the information that you did not know you needed. Carrier dialects
diverge. The first sign of a divergence is usually a message that the decoder
cannot read. If the bytes are gone, the evidence is gone.

Capture before decoding has these benefits:

- **Replay.** Fix the decoder, then reprocess the traffic that failed. The
  partner considers those messages delivered and does not resend them.
- **Audit.** The bytes answer the question of what the partner sent, years
  later. The answer does not depend on the decoder that was deployed at the
  time.
- **A safe failure mode.** A decode bug costs a reprocessing run. It does not
  cost a booking.

## The derived record

`pnr.state` is a projection of the events in `pnr_event`. The store keeps the
projection next to the events for cheap reads. Every event names the message
that caused it.

This link makes an interline dispute answerable. The record shows its current
contents. The events show which partner message put each item there, and when.
Without that link, reconciling a disagreement means reading 2 message logs side
by side and guessing.

## Concurrency

A gateway and a carrier can change one record at the same instant. For
example, a schedule change arrives while an agent adds a passenger. Every
write carries the version it read:

```go
rec, _ := store.GetPNR(ctx, locator)
expected := rec.Version
// ... apply changes ...
err := store.UpdatePNR(ctx, rec, expected, events)   // ErrConflict if it moved
```

`ErrConflict` means re-read and reapply. The pipeline does this automatically,
up to a bounded number of attempts. A blind write would discard the change
that it did not see. Nobody would discover the loss before a passenger did.

The store conformance suite asserts this against both backends. The suite
includes a concurrent-writers test that requires exactly 1 of 8 writers to
win.

## Layering

```
  cmd/jetwayd   cmd/carriersim   cmd/jetwayctl
        │              │               │
        └──────────────┴───────────────┘
                       │
   pkg/config ────┤
   pkg/ingress ───┤                          pkg/metrics
   pkg/egress ────┼── pkg/gateway ── pkg/transport
   pkg/spool ─────┤
   pkg/api ───────┘
                       │
                       pkg/store  (mem | postgres)
                                 │
   ┌─────────────────────────────┴──────────────────────────────┐
   │                          pkg/pnr                           │  canonical model
   ├──────────────────┬──────────────────┬──────────────────────┤
   │   pkg/airimp     │    pkg/padis     │      pkg/rescode     │  message grammars
   ├──────────────────┼──────────────────┼──────────────────────┤
   │   pkg/typeb      │   pkg/edifact    │                      │  wire syntax
   └──────────────────┴──────────────────┴──────────────────────┘
```

The codec packages under `pkg/` do not import the gateway, the store or the transport. `pkg/matip` imports the transport, because it is a transport.
You can use the codecs on their own. `jetwayctl decode` uses them this way,
and it works with no server running.

The design depends on the split between wire syntax and message grammar.
Syntax rules are universal and stable. For this reason, `pkg/typeb` and
`pkg/edifact` can be exact and strict in what they validate. Message
composition varies by carrier, version and bilateral agreement. `pkg/airimp`
and `pkg/padis` are therefore profiles. A profile is an ordered set of
recognisers and segment handlers that you can extend for each link.

An unknown message type still decodes at the syntax layer. The gateway can
therefore capture, route and replay it, even when no higher layer knows its
meaning.

## The shared status vocabulary

`pkg/rescode` holds the 2-letter action and status codes. The codes appear in
teletype segment elements and in EDIFACT `RPI` segments. Both decoders read
them from this one table.

A code table duplicated across 2 decoders drifts. If the 2 decoders disagree
about whether `US` means "waitlisted", their bookings disagree with the
partner that holds the other copy.

## Integration seams

| Seam | Interface | Default |
| --- | --- | --- |
| Seat inventory | `gateway.Responder` | `gateway.Inventory`, a simulator |
| Persistence | `store.Store` | `store.Mem` or `store.Postgres` |
| Inbound transport | `ingress.Ingress` | HTTPS, TCP, file drop |
| Outbound transport | `egress.Sender` | post, dial, reply-in-session, file drop |
| Link framing | `transport.Framer` | length-prefix or sentinel, from config |
| Teletype dialect | `airimp.Profile` | `airimp.Default` |
| EDIFACT dialect | `padis.Profile` | `padis.Default` |

`Responder` is the most important seam. The carrier decides whether a seat is
available. This project is not an inventory system. The interface is a single
synchronous call. The call receives a record and returns a status code for
each segment. Putting the carrier's inventory system behind the interface
changes nothing else.

## Routing

A message finds a link in 2 ways. The 2 ways answer different questions.

**By peer name** is the route for a reply. The pipeline already knows which
partner it is answering. **By address** is the route to every addressee of a
message. A Type B priority line can carry several addressees. The network is
expected to deliver a copy to each addressee. Routing by peer name alone
reaches only the single link that the message was given to.

`Gateway.Fanout` resolves each address through a table built from every peer's
`TTYAddress` and `Addresses`. It sends the bytes unchanged and reports a
result for each addressee. The result is delivered, terminates here, or served
by no link.

The gateway sends the bytes unchanged by design. If it rewrote the address
line for each recipient, each copy would be a different message from the
message in the log. The address line is also part of what a partner can
check.

`routing.relay` forwards traffic that is addressed to *other* links. With
relay on, the node is a switch. Otherwise the node is an endpoint. Relay is
off by default. A node that relays for anyone who can reach it is an open
relay. A partner can then spend another partner's link budget through this
node, under this node's originator address.

When relay is on, the most important addressee is the one that the relay
skips. That is the link the message arrived on. Forwarding to that link
returns the message to its sender. On a store-and-forward network, that loop
survives restarts.

## Queues

A record that needs human attention must reach a place where a person looks.
`pkg/queue` is that mechanism. It has 2 producers, and they place items for
different reasons.

The **gateway** places an item when a partner's answer changes a segment into
a state that somebody must act on. Such a state is a confirmation, a refusal,
a waitlist, or a status outside the interline vocabulary. The trigger is the
*transition*. The gateway captures the status before it applies the message
and compares the status after. If the trigger were the resulting state, every
later message that touched a settled record would re-raise the confirmation.

The **sweeper** places an item for conditions that arise because time passed.
A partner who answers creates work by answering. A partner who never answers
sends no event, and a ticketing deadline that expires sends no event either.
Only a periodic pass can detect those conditions.

Placement is idempotent on `(queue, record, reason code, segment)`. The
segment is part of the key because an interline record has one segment per
carrier, and each carrier answers separately. When 2 partners confirm one
booking, that is 2 items of work. Record-level placements, such as a
ticketing limit, use segment 0 and therefore still collapse to 1 item. In
Postgres the key is a partial unique index over pending rows. It is not a
lookup, because 2 racing sweepers must not both succeed.

Working an item does not delete it. After an interline dispute, the question
is who cleared the item and when.

### Queue state in the store

A reservations queue is a worklist. It is not a transport. People list, count,
filter, re-read and audit it after the fact. Those are database semantics.
They are not message semantics. The state therefore lives in
`store.QueueStore`, next to the records that it refers to.

An external queueing system is good at the other half of the job. It tells a
robot that work has arrived. `queue.Publisher` is that seam.

The store writes a placement first, and the publisher publishes it second.
This is the same order, for the same reason, as capture before decoding. A
publish that fails leaves work that the next reader still finds. A publish
that succeeded before the write would announce work that nobody can look up.
`pkg/queue` logs an error from the publisher and never propagates it. Failing
the placement would discard work that is already recorded.
