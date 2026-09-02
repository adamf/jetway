# Changelog

Jetway is pre-1.0: the API moves when the evidence says it should. Most of
what is fixed below was found by [wholesky](https://github.com/adamf/wholesky)
driving hundreds of embedded jetway assemblies through a simulated day of
global airline traffic -- the widening exercise surface is the test plan.

## v0.1.48 — One writer per system
- `config.Lease`: when enabled, a node binds its links only while it holds
  the system's lease in the store (`store.Leaser`, migration 0009), renews
  it at a third of the term, stands by while another process holds it, and
  takes over when the holder lets it lapse or releases on shutdown.
  `/readyz` is not ready while standing by. `node.Options.Store` lets a
  harness share one store between nodes, which is how it is tested.

## v0.1.47 — Readiness, retirement, and outbox metrics
- `/readyz` fails when the store cannot be reached; `store.Pinger` on every
  backend.
- `POST /api/admin/retire` and `jetwayctl retire --before` run retention as
  an operation; `store.Retirer` names the stores that retire by day.
- `jetway_outbox_depth{peer}` and `jetway_outbox_congested_total{peer}`.

## v0.1.46 — Revenue by leg
- `Store.RevenueByLeg`: what the live priced records paid per leg,
  operating carrier, flight, date and boarding point, a record's total
  shared evenly across its air segments. A revenue ledger rebuilds from it
  when a system starts with a book already full.

## v0.1.45 — v0.1.44, with its test right
- v0.1.44 shipped with a test that wanted the coupon reference stripped
  from the ticket; departure control keeps it, as the ETL needs it. Use
  this release.

## v0.1.44 — Elements that name their passenger
- A name item's element may end with the passenger it belongs to, written
  as the item writes the name -- `.R/CHLD HK1 SMITH/TIMMSTR`, `.R/TKNE HK1
  5262000000003C1 SMITH/TIMMSTR` -- and departure control then attaches it
  to that passenger alone. Before this a family's CHLD made every name a
  child, an INFT made every name an infant, and one ticket covered the
  item. An element naming nobody stays the party's. Ticket numbers with the
  airline code hyphenated off are recognised.

## v0.1.43 — Retirement reclaims the default partitions
- `RetireBefore` truncates an emptied default partition, so the space a
  converted book left behind comes back at once instead of waiting for
  vacuum.

## v0.1.42 — Fares, pricing, and the class ladder
- `pkg/fare`: a filing of fares per market and class with basis codes,
  amounts and rules (advance purchase, stay, season, refundability, change
  fee), taxes by kind (percent of base, per segment, per ticket, per
  enplanement), passenger-type discounts, and `Price`, which sells each
  segment under the cheapest fare in its class whose rules the trip meets
  and refuses with the rule named when none does. Carries no fare of its
  own: the filing is the deployment's.
- `Gateway.Tariff` prices every booking the node makes: fare basis on the
  segment, `pnr.Pricing` on the record, the value on each ticket coupon.
  A class with no sellable fare refuses the booking; the wrapped error is a
  `*fare.ErrNoFare`.
- `inventory.Levels`: nested booking-class authorisations per cabin, the
  ladder a revenue management system publishes. A class closes when its
  authorisation is used by it and the classes beneath it, while the cabin
  still sells higher classes; availability reports the class's seats.

## v0.1.41 — The cancelled flight's passenger list
- `Store.FindPNRsEverOnFlight`: every record that ever held a segment on a
  flight, cancelled segments and cancelled records included. The live
  lookup correctly forgets a passenger once their segment is XX; the
  flight's history should not.

## v0.1.40 — Records retire by partition
- Records and their events are partitioned by `purge_at`, the day a record
  may be retired: its last flight plus `Postgres.RetireGrace` (three days
  by default), or a year after creation with no flight. Daily partitions
  are created on demand; `Postgres.RetireBefore` drops every day that has
  passed and clears the queue items left pointing at nothing. Migration
  0008 converts a populated book in place, inside one transaction.
- Locators stay unique per system across partitions by an explicit check;
  the unique index carries the partition key.
- `Purge` (the per-system delete) now takes events and queue items with
  the records, since the foreign keys had to go.
- The migration runner takes an advisory lock and re-checks, so two
  processes booting against one database take turns.

## v0.1.39 — IROPS waits for the answer
- `irops.Engine` treats a seat it asks for as a request until the carrier
  answers. Confirmed: the dead leg and any waitlist this pass took out are
  cancelled and the item is worked. Waitlisted: kept, and the search goes
  on; if nothing confirms, `Rebook` returns `ErrWaitlisted` with the
  waitlists named and the item stays for a person. Refused or unanswered
  within `ReplyTimeout`: the request comes off the record. Before this the
  engine dropped the dead leg on the strength of a request, which, against
  a real inventory, left a passenger holding nothing.

## v0.1.38 — Inventory pools by leg
- The inventory keys a cabin by flight, date and boarding point, and
  `Store.SoldSeats` groups by board: a carrier flies one number over
  several legs a day, and each leg is its own aircraft.

## v0.1.37 — A seat inventory
- `pkg/inventory`: a carrier's seats per cabin. Capacity comes from the
  schedule per flight, date and compartment; booking classes draw on their
  cabin's pool; a full cabin waitlists to a depth, then refuses; a flight the
  carrier does not fly is UN. It answers as `gateway.Responder`, broadcasts
  availability with the seats left, and is rebuilt from the book of record:
  `Seed` counts what a stored segment holds and `Store.SoldSeats` sums a
  carrier's holdings per flight, date, class and status in one query.
- `gateway.Releaser`: a Responder that wants to know when an inbound
  cancellation turned a holding into XX, so the seat sells again. The
  inventory implements it.
- `gateway.Inventory` stays as the demo stand-in it always was.

## v0.1.36 — Links never wait on the peer's window
- Every link's writes go through `transport.Outbox`: a bounded queue drained
  by one writer goroutine, in `transport.Link`, the TCP ingress session and
  the MATIP session. Both ends of a link run their handler inside the read
  loop and often answer what they read; with writes inline, a reader waiting
  for the socket and a peer doing the same was a deadlock that only the
  thirty-second write deadline broke. Filling a recorded day to a holiday
  load found it in the first departure bank. `Send` now returns
  `ErrCongested` when the queue has been full for `SendTimeout`, and fails
  at once while the peer stays congested, so a stopped peer costs a message
  an error rather than a stall. `OutboxDepth` and `SendTimeout` are tunable.

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
