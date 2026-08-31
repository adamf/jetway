# Architecture

## The inbound pipeline

Every inbound message goes through the same stages in the same order. The
ordering is the contract, and it is enforced in `pkg/gateway.Ingest`.

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

A failure at any stage after capture leaves the message in the dead letter queue
with its bytes intact and the reason recorded. It is never dropped and never
silently succeeds.

## Why capture comes first

The alternative — parse, then store what you understood — loses the information
you did not know you needed. Carrier dialects diverge, and the first sign is
usually a message you cannot read. If the bytes are gone, so is the evidence.

Capturing first buys three things:

- **Replay.** Fix the parser, reprocess the traffic that failed. The partner
  considers those messages delivered and will not resend them.
- **Audit.** "What did they actually send" is answerable years later, and the
  answer does not depend on the parser deployed at the time.
- **A safe failure mode.** A decode bug costs a reprocessing run, not a booking.

## Why the record is derived

`pnr.state` is a projection of the events in `pnr_event`, kept alongside them
for cheap reads. Every event names the message that caused it.

This is what makes an interline dispute answerable. The record shows what it
holds now; the events show which partner message put it there and when. Without
that link, reconciling a disagreement means reading two message logs side by
side and guessing.

## Concurrency

A gateway and a carrier can be changing one record at the same instant — a
schedule change arriving while an agent adds a passenger. Every write carries
the version it read:

```go
rec, _ := store.GetPNR(ctx, locator)
expected := rec.Version
// ... apply changes ...
err := store.UpdatePNR(ctx, rec, expected, events)   // ErrConflict if it moved
```

`ErrConflict` means re-read and reapply, which the pipeline does automatically
up to a bounded number of attempts. A blind write would silently discard
whichever change it did not see, and nobody would find out until a passenger did.

The store conformance suite asserts this against both backends, including a
concurrent-writers test that requires exactly one of eight writers to win.

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

The `pkg/...` tree does not import the gateway, the store or the transport. You
can use the codecs on their own — `jetwayctl decode` does exactly that, and
works with no server running.

The split between wire syntax and message grammar is load-bearing. Syntax rules
are universal and stable, so `pkg/typeb` and `pkg/edifact` can be exact and
strict about what they validate. Message composition varies by carrier, version
and bilateral agreement, so `pkg/airimp` and `pkg/padis` are profiles: ordered
recognizers and segment handlers you can extend per link. An unknown message
type still decodes at the syntax layer, so it can be captured, routed and
replayed even when nothing above knows what it means.

## The shared status vocabulary

`pkg/rescode` holds the two-letter action and status codes. They appear in
teletype segment elements and in EDIFACT `RPI` segments alike, and both decoders
read them from the one table.

That is not tidiness. A code table duplicated across two decoders drifts, and a
decoder that disagrees with its sibling about whether `US` means "waitlisted"
produces bookings that quietly disagree with the partner holding the other copy.

## Where your systems plug in

| Seam | Interface | Default |
| --- | --- | --- |
| Seat inventory | `gateway.Responder` | `gateway.Inventory`, a simulator |
| Persistence | `store.Store` | `store.Mem` or `store.Postgres` |
| Inbound transport | `ingress.Ingress` | HTTPS, TCP, file drop |
| Outbound transport | `egress.Sender` | post, dial, reply-in-session, file drop |
| Link framing | `transport.Framer` | length-prefix or sentinel, from config |
| Teletype dialect | `airimp.Profile` | `airimp.Default` |
| EDIFACT dialect | `padis.Profile` | `padis.Default` |

`Responder` is the important one. Deciding whether a seat is available is the
carrier's business and this project does not try to be an inventory system. The
interface is one synchronous call — given a record, return a status code per
segment — so putting a real inventory behind it changes nothing else.

## Routing

There are two ways a message finds a link, and they answer different questions.

**By peer name** is how a reply goes back: the pipeline already knows which
partner it is talking to. **By address** is how a message reaches everybody it
was sent to. A Type B priority line may carry several addressees and the network
is expected to deliver a copy to each; routing on peer name alone can only ever
reach the one link a message was handed to. `Gateway.Fanout` resolves each
address through a table built from every peer's `TTYAddress` and `Addresses`,
sends the bytes unchanged, and reports per addressee — delivered, terminates
here, or served by no link.

The bytes go out unchanged deliberately. Rewriting the address line per
recipient would make each copy a different message from the one in the log, and
the address line is part of what a partner may check.

Forwarding traffic addressed to *other* links — being a switch rather than an
endpoint — is `routing.relay`, and it is off by default. A node that relays for
anyone who can reach it is an open relay: a partner can spend another partner's
link budget through us, under our originator address. When it is on, the
addressee that matters most is the one skipped, because forwarding to the link a
message arrived on returns it to its sender, and on a store-and-forward network
that loop survives restarts.

## Queues

A record that needs human attention has to end up somewhere a human looks.
`pkg/queue` is that mechanism, and it has two producers with different
characters.

The **gateway** places on a queue when a partner's answer changes a segment into
a state somebody must act on: a confirmation, a refusal, a waitlist, a status
outside the interline vocabulary. The trigger is the *transition*, captured
before the message is applied and compared after. Using the resulting state
instead would re-raise a confirmation every time any later message touched a
settled record.

The **sweeper** places on a queue for things that happen because time passed.
A partner who answers creates work by answering; a partner who never answers
creates none, and neither does a ticketing deadline expiring, because neither
is an event anybody sends. Only a periodic pass can see those.

Placement is idempotent on `(queue, record, reason code, segment)`. The segment
is part of that key because an interline record has one segment per carrier and
each answers separately: two partners confirming one booking are two pieces of
work. Record-level placements such as a ticketing limit use segment 0 and so
still collapse to one. In Postgres this is a partial unique index over pending
rows, not a lookup, because two sweepers racing must not both succeed.

Working an item does not delete it. Who cleared it and when is the question
asked after an interline dispute.

### Why the state is not in a broker

A reservations queue is a worklist, not a transport. It is listed, counted,
filtered, re-read, and audited after the fact — database semantics, not message
semantics. So the state lives in `store.QueueStore` next to the records it
refers to.

What an external queueing system is good at is the other half: telling a robot
that work has arrived. `queue.Publisher` is that seam. A placement is stored
first and published second, in the same order and for the same reason as
capture-before-parse: a publish that failed leaves work that the next reader
still finds, whereas a publish that succeeded before the write would announce
work nobody can look up. An error from the publisher is logged, never
propagated, because failing the placement would discard work already recorded.
