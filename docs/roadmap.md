# Known gaps

This document lists the gaps in jetway. The items in the first 2 sections
block a production deployment.

The recently closed items follow. Departure control is closed. `pkg/dcs`
opens a flight from the PNL, accepts and seats passengers, tags bags, boards
and closes. It builds PFS, PTM, PSM, ETL, LDM and CPM with an AHM 560-method
loadsheet. The gateway's `Ground` seam hands it the airport-side traffic. The
switch now routes a carrier's unregistered station addresses down its own
link.

Split and divide are closed, with seats apportioned and every per-passenger
reference remapped. EMD is closed. It covers associated and standalone
documents, reason for issuance, association to flight coupons, and lifting
with them. Interline ticket control is closed. TKCREQ and TKCRES let a ticket
exist somewhere other than the node that issued it. The published coupon
status vocabulary replaces a guessed one.

Cancellation is closed. A carrier can now be told that a booking is off. This
unblocked NDC order cancellation, auto-cancel on a ticketing limit, and
cancelling from the console. Ticketing is closed. It covers document numbers,
coupons, conjunction sets, and issuance that satisfies a ticketing time
limit.

Also closed are priority-ordered redelivery and channel sequence gap
detection. CONTRL is sent and consumed. SSM and ASM are closed, with schedule
changes matched against held records. NDC order messages over HTTP are
closed. Address-based routing has multi-addressee fan-out and opt-in relay.
Work queues have a time-based sweeper and an external-publisher seam.

The Type B 4 KB message limit and the PDM possible-duplicate indicator are
closed. AVS ingestion has an availability cache and free sale. MATIP
(RFC 2351) has the Type B session handshake. Partner-facing ingress runs over
HTTPS, TCP and file drop. Peer identity comes from mutual TLS. Outbound retry
has restart recovery.

A durable inbound spool, health, readiness and metrics endpoints, graceful
drain, and container images are closed.

## Items blocked on missing documents

The largest single gap in this project is not code. Several message layers
are defined in paid publications that were not bought. These layers are
implemented as extensible profiles, built from public material and inferred
from message shapes. They work, and they are not conformant. No amount of
testing here can close that difference, because the tests would only check
the same guess twice.

| Document | What it costs us |
| --- | --- |
| **A4A/IATA AIRIMP** | `pkg/airimp` is a profile and is not conformant. **The divide message is missing entirely.** For this reason a split booking cannot be advised to its carriers. See below for what is and is not known about it. Element layouts and the rules governing action-code usage are inferred. |
| **IATA SSIM** | `pkg/ssim` knows the SSM and ASM action vocabulary, which is public, and infers the field layout within each line. |
| **IATA PADIS message directories** | `pkg/padis` segment layouts are inferred: `PAOREQ`, `PAORES`, and the `TKT`/`CPN` segments of `TKCREQ`/`TKCRES`. The *code sets* are public and are used. The message structures are not. |
| **The EMD System Update message** | Association and disassociation are recorded locally and the carrier advised over ticket control, because the guide names a distinct request without giving its EDIFACT form. |
| **BATAP** | There is no acknowledgement contract above MATIP. A relayed message therefore has no responsibility transfer, and a detected sequence gap cannot become a retransmission request. |
| **IATA RP 1719 (PFS) and RP 1719c (ETL)** | `pkg/dcs` builds and parses both as inferred layouts following the PNL family. The category vocabulary is public. The line layout is the guess. No free reproduction was found where the PSM, PTM, LDM and CPM all had one. |
| **IATA RP 1715 (PSM), RP 1718 (PTM), AHM 583 (LDM), AHM 587 (CPM)** | These are built from the practices' own worked examples as airports and handlers reproduce them, and tested against those verbatim. They are close, and still a profile. The element directory behind the examples was not read. |
| **IATA AHM 560** | The index arithmetic is the published method. The aircraft data in `dcs.DefaultFleet` is representative type-class data and not an operator's data. A deployment supplies its own. |
| **ARINC 618/620** | `pkg/acars` reads the OOOI reports a provider forwards, from OAG's verbatim examples. The air-ground leg (618) and the company formats (A80 and similar) are not modelled. |
| **ATPCO Optional Services** | Reason-for-issuance sub-codes are carried as free text rather than validated. The 7 top-level groups are public and are enforced. |

This has 2 consequences.

**The divide message is the most expensive single absence.** It is the last
of the cases in which this node changes something and cannot tell the
carriers. Cancellation was another such case, and building the cancel
message closed 3 separate blocked features at once. This node can split a
booking correctly while the carriers still hold one record covering both
halves. Every division records that as a divergence.

The free AIRIMP table of contents narrows the gap. The message exists and is
named in **§3.7.10, Divided PNR Message (DVD)**. A procedure chapter for it
is at **§3.4, Dividing Party**, with worked examples at §3.4.6. The DVD
element layout and the exchange around it are behind the paywall. The
unknowns are what the message carries, and whether the partner returns a
locator for the divided record.

This has 2 further consequences. First, buying the manual would not close the
gap completely. **§7.17 is titled "Divide Made by Member (Bilateral)"**, and
§7.18 likewise. Part of divide handling is therefore agreed per partner
rather than universal. `airimp.Profile` exists for that purpose. Second, the
EDIFACT half is **not blocked**.

The free PNRGOV implementation guide documents how a split PNR is
represented. `GR.8` carries the split record locators. `EQN` (§5.6) carries
the number of passengers split from or to the record. `RCI` carries the
locators themselves. That is enough to build the PADIS side against a free
source. Only the teletype side waits on a purchase.

**Unaffected layers.** ISO 9735, CONTRL, MATIP and the NDC schemas are all
published, and those layers are checked rather than inferred. The coupon
status vocabulary is also checked, against the free EMD guide. Checking it
against that source corrected 3 errors in a list that had been guessed. Where
a free source exists, it is used, and it has found bugs every time.

## Blockers for handling passenger data

- **Field-level encryption at rest.** `DOCS`, `DOCA`, `DOCO` and `FOID` are
  flagged `Sensitive` and redacted from logs and the console. The store holds
  them in plaintext. There is no key management.
- **Retention and erasure.** `Store.Purge` discards everything older than a
  given instant, per node, on both backends. It is the tool for a retention
  policy. No policy runs it in `jetwayd` yet, and there is no erasure of one
  person's data on request.
- **Access control.** The API and console are unauthenticated. There is no
  concept of who may read a record.

## Blockers for a production link

- **IBM MQ transport.** Many carriers offer MQ rather than a socket. Ingress
  is an interface and MQ fits it. Nobody has written it.
- **SITA and ARINC network access.** Reaching a carrier over Type B needs a
  commercial contract and an assigned address rather than code. The protocol
  side is done. The procurement side is not.
- **MATIP Type A.** Only Type B is implemented. Type A carries interactive
  terminal traffic on port 350 and shares the header format.
- **BATAP.** MATIP names it as the messaging responsibility transfer
  protocol. The acknowledgement semantics above MATIP are not implemented.
- **Backpressure.** A listener paces each peer (`rate_limit`, `burst`) and
  the listener as a whole (`total_rate_limit`), caps its connections
  (`max_connections`) and reaps idle links (`idle_timeout`). There is no
  per-peer quota on bytes or on stored records.
- **Certificate rotation.** The process reads server and client certificates
  once at start. Rolling a certificate needs a restart.

## Protocol coverage

- `PNRGOV` and `PAXLST` are built and read (`pkg/padis`, `pkg/paxlst`).
  `DCQCKI`/`DCRCKA` are built and read too (`pkg/iatci`). `TKCREQ`/`TKCRES`
  and the rest decode at the syntax layer and route, but they do not map
  onto a record.
- **`AVS`**, `CONTRL`, `SSM` and `ASM` are implemented. A partner cannot yet
  ask for a CONTRL on a *functional group*. UCF is built, but no path
  produces one. Schedule messages update no schedule of our own. They only
  raise work against records.
- **Departure control** handles single-leg flights, with one boarding point
  and one destination per flight. Multi-sector flights are not modelled.
  Multi-sector features are through passengers, per-destination PSMs, and
  SOM seat-occupied messages to the next station. The `Store` seam has only
  the in-memory implementation. A Postgres implementation is a table with a
  key and a jsonb column. UCM (ULD control), PRL (passenger reconcile list)
  and the EDIFACT check-in pair (`DCQCKI`/`DCRCKI`) are not consumed.
- **The divide message, on teletype only.** An EDIFACT partner is now advised
  of a division. A teletype partner is not, because the AIRIMP message is in
  a manual this build does not have. Both substitutes are wrong. Selling the
  child again double-books, and cancelling and reselling risks losing the
  seats. Those carriers still hold one record, and each raises a divergence
  that says so.
- **No reply is expected to a divide advisory.** The partner is told. Whether
  the partner returns a locator for the divided record, and what this node
  should do with it, is part of the paywalled procedure.
- **NDC shopping.** Orders are implemented. AirShopping and OfferPrice are
  not implemented and will not be. An offer is priced, pricing needs fares,
  and fares are out of scope. This node refuses an OrderCreateRQ that names
  only an offer this node never made, and the refusal says so.
- **NDC 21.3.** The EDIST generation is implemented. The 21.3 generation
  renamed the messages and restructured the payload. It is a different
  mapping.
- **NDC cancellation** works. When a carrier could not be told, the response
  is a 202 that carries both the order and an error. Reporting only the
  success would tell the requester that their seats are released when they
  may not be.
- **ONE Order.** Work has not started.
- **EMD sub-codes.** The reason-for-issuance groups are enforced. The
  sub-code list is ATPCO's, and the node carries it as free text rather than
  validating it.
- **The System Update message.** The node records association locally and
  advises the carrier over ticket control. The guide names a distinct request
  for it without giving its EDIFACT form.
- **Ticket control is EDIFACT only.** A teletype partner cannot be told a
  ticket covers their segment, because there is no equivalent message. Those
  carriers land on the divergence queue at issuance instead.
- **No refund or exchange.** A carrier can report coupons as refunded or
  exchanged, and this node records the report. Originating either is a fares
  operation.
- **Auto-cancel is opt-in.** `Sweeper.Cancel` cancels a booking whose
  ticketing limit has passed and tells the carriers. It is nil by default.
  Giving seats back has consequences, and a deployment should ask for it
  rather than discover it.

## Switching

Jetway now routes on the Type B address line as well as by peer name. It
delivers a message with several addressees to each of them
(`Gateway.Fanout`). With `routing.relay` on, it forwards traffic addressed to
other links. A commercial switch still does the following, and this does not:

- **Priority is banded rather than ranked.** Redelivery now services urgent
  before normal, and normal before deferred. The bands are deliberate.
  Published material names the codes but does not settle a total order. The
  order of `QX` against `QK` is therefore not claimed.
- **Sequence gaps are detected but not recovered.** The gateway reports a
  hole in a link's numbering on the message and counts it. Nothing requests a
  retransmission, because that needs BATAP. The baseline is in memory. A
  restart therefore loses a link's position rather than inventing continuity
  across the restart.
- **No multi-part split or reassembly.** The limit of 60 lines by
  63 characters means that a long passenger list arrives as `PART1`, `PART2`.
  Encode refuses to build an over-long message, which is correct. Nothing
  splits a long message or joins the parts back.
- **No undeliverable queue for transit.** The gateway records an address that
  nothing serves on the message and logs it. A switch parks such a message
  for an operator.
- **BATAP** is still unimplemented, and relay therefore has no responsibility
  transfer. The gateway forwards and records, but there is no acknowledgement
  contract above MATIP.

## Queues

Queues exist (`pkg/queue`, `store.QueueStore`) and the console shows them.
The gaps are:

- **Queue numbering.** Names are Jetway's own vocabulary. A deployment that has
  to match a house convention needs a name-to-number mapping at the edge.
- **The sweeper scans.** `Sweeper.Sweep` lists records and filters in Go. That
  is fine at demo volume and wrong at scale. At scale, the due-date
  predicates belong in an indexed query. `Limit` bounds the damage in the
  meantime.
- **One queue-driven robot.** `irops.Engine` works the schedule-change queue
  for cancellations. It takes only seats the availability cache shows open,
  unless configured to ask carriers. Retimings, waitlist clearance and the
  ticketing queue still wait for a person.
- **Waitlist clearance and schedule change** are the 2 producers that would
  make queues useful, and neither exists yet.

## Scale

[scaling.md](scaling.md) has the measurements. The main finding is that the
record scans on the hot path are **correctness** bugs before they are
performance bugs. `ListPNRs` orders by `updated_at` and takes a limit.
`findTicket` and `findByExternalLocator` therefore cannot see an older
booking at all, and they refuse a partner's message with something false. Fix
those before anything else.

- **3 hot-path lookups scan the record table.** The cost is linear, at 4 ms
  and 8 MB for 10,000 records. They need indexed queries against the document
  number and the carrier locator.
- **The sweeper reads records and filters in Go.** The due-date predicates
  belong in SQL.
- **`api.insights` aggregates per request.** That is right for a demo and
  wrong anywhere else. It should read counters.
- **The availability cache is per process**, and 2 processes therefore
  disagree about what is sellable. That is the main obstacle to running more
  than one process.
- **Many systems, one database** is supported by `Postgres.Node`. Rows carry
  the system they belong to, and a view sees only its own rows. `store.Split`
  keeps the message log elsewhere while the records live in Postgres. In
  wholesky, the message log is in bounded memory. 500 systems' wire bytes are
  not a row-per-message workload.
- **Channel sequence baselines are per process** and are lost on restart. A
  failover therefore reports a gap that is not there.
- **The message log is not partitioned.** It is append-only with a
  time-ordered key, which suits a range partition. Partitioning would make
  retention a `DROP TABLE`.

## Engineering

- **The spool is not bounded.** It grows until the disk is full. A depth
  limit that refuses new entries rather than filling the volume would be
  better.
- **Spool draining is serial.** One slow message holds up the queue behind it.
- **The AIRIMP profile is thin.** It covers the elements a reservation gateway
  must act on. Expect to extend it per link.
- **Locator normalisation is strict.** `pnr.NormaliseLocator` deliberately
  rejects characters outside the alphabet rather than correcting them. There
  is no fuzzy lookup for an agent working from a bad transcription.
- **The demo fleet lives in the shipped binary.** `pkg/demo` should be
  built separately from a production `jetwayd`.

## Scope exclusions

Fares and pricing are out of scope. Jetway is a messaging gateway, a record
store, a seat inventory and a departure control system. It is not a pricing
engine. `gateway.Responder` is the seam where an inventory system plugs in,
and `gateway.Ground` is the seam where an airport plugs in. Neither seam
handles the price of a seat.
