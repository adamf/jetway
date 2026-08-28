# Architecture

## The inbound pipeline

Every inbound message goes through the same stages in the same order. The
ordering is the contract, and it is enforced in `internal/gateway.Ingest`.

```
bytes from a link
      │
      ▼
┌─────────────┐  the raw bytes and their digest are written and durable before
│ 1. capture  │  anything reads them. The transport acknowledges the peer only
└─────────────┘  after this succeeds.
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
└─────────────┘  and digest for teletype, which carries no such field.
      │
      ▼
┌─────────────┐  resolve the record by locator, fold the message into it, write
│ 5. apply    │  the new state with the version it was read at. On conflict,
└─────────────┘  re-read and reapply.
      │
      ▼
┌─────────────┐  only for message classes that expect an answer. The decision is
│ 6. respond  │  recorded on our own record before the answer is sent, so our
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
        internal/api ──┴── internal/gateway ── internal/transport
                                 │
                       internal/store  (mem | postgres)
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
| Link transport | `transport.Framer`, `transport.Sender` | length-prefix over TCP |
| Teletype dialect | `airimp.Profile` | `airimp.Default` |
| EDIFACT dialect | `padis.Profile` | `padis.Default` |

`Responder` is the important one. Deciding whether a seat is available is the
carrier's business and this project does not try to be an inventory system. The
interface is one synchronous call — given a record, return a status code per
segment — so putting a real inventory behind it changes nothing else.
