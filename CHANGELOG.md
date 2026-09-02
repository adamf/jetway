# Changelog

Jetway is pre-1.0: the API moves when the evidence says it should. Most of
what is fixed below was found by [wholesky](https://github.com/adamf/wholesky)
driving hundreds of embedded jetway assemblies through a simulated day of
global airline traffic -- the widening exercise surface is the test plan.

## v0.1.35 — Parts count lines, not names (v0.1.34 shipped with the guard missing; do not use it)
- `pnl.BuildParts` paginates on the lines a folded item renders to, so a
  cabin of families with tickets still fits every part inside the Type B
  envelope. The v0.1.33 fold made a part of fifty names run to seventy
  lines.

## v0.1.33 — Long name items fold
- A name item that does not fit a Type B line -- a family of six with a
  ticket number each -- now folds onto continuation lines indented one
  space: more given names begin with a slash, more elements with a dot.
  `pnl.Parse` reads the continuation back into one item, `pnl.NameLines`
  is the folded form, and the PSM and PTM write it too. Found by filling a
  recorded day to a holiday load: the first real family broke the encoder.

## v0.1.32 — Loading a book of record
- `Store.LoadPNRs` stores many new records in one batch, each at version 1
  with one `loaded` event naming the actor. Postgres streams both tables
  with `COPY` in one transaction; a locator already held fails the whole
  batch and stores nothing. For migrations, restores, and a day's bookings
  seeded before the day runs.
- `gateway.Ground` documentation names the datalink and ATS traffic it
  hands over, alongside the name lists, bag messages and departure output.

## v0.1.24 — Many systems, one database
- `Postgres.Node` returns a view scoped to one system: rows carry the
  system they belong to and every query sees only its own. Migration 0007
  adds the column and makes locators unique per system.
- `store.Split` keeps the message log in one store and the records in
  another.
- `Store.Purge` discards everything older than a moment, per node, on both
  backends.

## v0.1.23
- A movement, name list, bag message or departure message that is
  recognised and then fails to parse goes to the dead letter queue with the
  parser's reason, instead of falling through to the booking grammar.
- PSM names may carry digits.

## v0.1.22
- Relay reflects only a message addressed to its own origin address. A
  message from one of a link's addresses to the link's principal address --
  check-in sending final sales home to reservations -- is delivered.

## v0.1.21 — Departure control
- New `dcs` package: a Station opens flights from the PNL, applies ADLs,
  accepts and seats passengers, tags bags, boards, offloads, closes; builds
  PFS, PTM, PSM, ETL, LDM and CPM; load control by the AHM 560 index method
  with a loadsheet. PSM/PTM/LDM/CPM follow published worked examples; PFS
  and ETL are inferred and labelled so.
- `gateway.Ground` is the seam that hands name lists, bag messages and
  departure output to a consumer; a refusal is recorded against the message
  as rejected with the bytes kept.
- `PeerByAddress` falls back to the carrier whose designator an address
  carries, and relay delivers to other addresses on the arrival link.
- The console gains a Departures view on `/api/dcs/...`.
- `pnl.ParseName` and `pnl.NameLine` are exported for the list family.

## v0.1.15 – v0.1.20
- One shared listener identifies plain-TCP subscribers by their hello
  frame (`by_hello`), so a switch serves a population on one port.
- An evicted message's outcome is not an error to log.
- The switch's bus publishes movements in transit, so an operations display
  can watch the whole sky from the switch.
- The late-confirmation resurrection classes: a KK arriving after an XX no
  longer revives a cancelled booking, and a stray reply matching only an
  external locator is resolved amend-only and peer-scoped.

## v0.1.14
- The `/api/insights` aggregate serves a short-TTL snapshot, so twenty open
  consoles cost one computation instead of twenty.
- Migration 0006 partitions the Postgres message log by time when it is
  empty -- a month of history becomes a partition to drop, not millions of
  rows to delete. A log that already holds data is left alone with a notice.

## v0.1.13 — Name lists and bag messages
- New `pnl` package: the Passenger Name List and Additions and Deletions
  List (RP 1707/1708, reconstructed from free documentation), with Type B
  envelope partitioning.
- New `baggage` package: the Baggage Source and Baggage Processed messages
  (RP 1745), unknown elements carried verbatim.
- The gateway classifies both on ingest and keeps them away from the
  booking grammar.

## v0.1.12 — The availability cache advises; it never bars the door
- A locally exhausted free-sale count no longer fabricates a "carrier
  reported closed" refusal; the next booking asks the carrier.
- A genuine carrier Closed bars free sale but not the request -- the
  carrier answers, often with the waitlist the old shortcut denied.

## v0.1.11 — MATIP client
- The MATIP package gained the initiating side: a reconnecting client with
  the same contract as the plain transport client, so airline hosts can
  dial in over RFC 2351.

## v0.1.10 — Names outside the wire charset are refused at the counter
- A surname no wire format can carry (underscores, accents) is refused at
  booking validation instead of being accepted and stranded at HN forever.

## v0.1.9 — A clock seam
- Gateway, Bus and both stores take an optional `Now func() time.Time`;
  every stamped timestamp reads through it, so simulations can drive time
  and replays can pin it.

## v0.1.8 — LivePeers means sessions
- `LivePeers` no longer folds in the router's configured egress list, which
  never shrinks; a dead link now reads as dead.

## v0.1.7 — Idle links die with their context
- Both transport sides close the conn via `context.AfterFunc`, so
  cancelling a quiet link actually takes it down.

## v0.1.6 — Cancellations stay cancelled
- EDIFACT cancels carry message function 1 (cancellation) and are applied
  as advisories, never answered as sells.
- `Recompute` derives liveness from the rescode vocabulary: refusals and
  cancellations are dead, unknown codes stay live.

## v0.1.5 and earlier
- v0.1.5: bus publishes under the read lock (subscribe/publish race).
- v0.1.4: the console serves from any path prefix.
- v0.1.3: movement events carry the diversion airport.
- v0.1.2: flight keys split at the designator, so U2 and other
  alphanumeric designators route correctly.
- v0.1.1: embedders can extend the console mux.
- v0.1.0: first public cut -- Type B/AIRIMP and EDIFACT/PADIS gateways, the
  PNR store, queues, sweeps, AVS, MVT, SSM/ASM, NDC, MATIP ingress, the
  live console.
