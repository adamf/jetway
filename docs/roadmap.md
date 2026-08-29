# What is missing

An honest list. Things in the first two sections block a production deployment.

Recently closed: ticketing -- document numbers, coupons, conjunction sets, and
issuance that satisfies a ticketing time limit; priority-ordered redelivery and
channel sequence gap detection; CONTRL, sent and consumed; SSM and ASM, with schedule changes
matched against held records; NDC order messages over HTTP;
address-based routing with multi-addressee fan-out and opt-in
relay; work queues with a time-based sweeper and an external-publisher
seam; the Type B 4 KB message limit and the PDM possible-duplicate indicator;
AVS ingestion with an availability cache and free sale;
MATIP (RFC 2351) with the Type B session handshake;
partner-facing ingress over HTTPS, TCP and file drop; peer identity from
mutual TLS; outbound retry with restart recovery; a durable
inbound spool; health, readiness and metrics endpoints; graceful drain;
container images.

## Blocks handling real passenger data

- **Field-level encryption at rest.** `DOCS`, `DOCA`, `DOCO` and `FOID` are
  flagged `Sensitive` and redacted from logs and the console, but they are
  stored in plaintext. There is no key management.
- **Retention and erasure.** No expiry, no purge, no erasure workflow. The
  message log keeps raw bytes indefinitely, and those bytes contain personal
  data regardless of what the projection redacts.
- **Access control.** The API and console are unauthenticated. There is no
  notion of who may read a record.

## Blocks a production link

- **IBM MQ transport.** Many carriers offer MQ rather than a socket. Ingress is
  an interface and MQ fits it; nobody has written it.
- **SITA and ARINC network access.** Reaching a carrier over Type B needs a
  commercial contract and an assigned address, not code. The protocol side is
  done; the procurement side is not.
- **MATIP Type A.** Only Type B is implemented. Type A carries interactive
  terminal traffic on port 350 and shares the header format.
- **BATAP.** MATIP names it as the messaging responsibility transfer protocol;
  the acknowledgement semantics above MATIP are not implemented.
- **Backpressure.** No admission control if a partner floods a link.
- **Certificate rotation.** Server and client certificates are read once at
  start. Rolling one means a restart.

## Protocol coverage

- `PNRGOV`, `PAXLST`, `TKCREQ`/`TKCRES`, `DCQCKI`/`DCRCKI` decode at the syntax
  layer and route, but do not map onto a record.
- **`AVS`**, `CONTRL`, `SSM` and `ASM` are implemented. What is not: a partner
  cannot yet ask for a CONTRL on a *functional group*, since UCF is built but
  no path produces one, and schedule messages update no schedule of our own --
  they only raise work against records.
- **`PNL`/`ADL`** passenger lists to departure control are not implemented.
- **Split and divide.** `StatusSplit` exists in the model; the operation does
  not.
- **NDC shopping.** Orders are implemented; AirShopping and OfferPrice are not
  and will not be. An offer is a priced thing, pricing needs fares, and fares
  are out of scope. An OrderCreateRQ naming only an offer this node never made
  is refused saying exactly that.
- **NDC 21.3.** The EDIST generation is implemented. The 21.3 generation
  renamed the messages and restructured the payload; it is a different mapping.
- **NDC cancellation** stops at our own record: the carriers are not told, so
  the request is refused rather than left diverging from what they hold.
- **ONE Order.** Not started.
- **Ticketing is local only.** Documents are issued, numbered and couponed, and
  issuance clears the time limit. What is missing is the wire: no TKCREQ or
  TKCRES to a ticketing system, no interline e-ticket exchange, no EMD, and no
  coupon status changes driven by a partner. Nothing tells anybody else that a
  ticket exists.
- **No auto-cancel on expiry.** The sweeper raises a passed time limit and
  stops there. Cancelling locally while the carriers still hold the seats is
  the same divergence the NDC cancel path refuses to create, so the
  cancellation message has to come first.

## Switching

Jetway now routes on the Type B address line as well as by peer name: a message
with several addressees is delivered to each (`Gateway.Fanout`), and with
`routing.relay` on it forwards traffic addressed to other links. What a real
switch still does and this does not:

- **Priority is banded, not ranked.** Redelivery now services urgent before
  normal before deferred. The bands are deliberate: published material names
  the codes but does not settle a total order, so `QX` against `QK` is not
  claimed.
- **Sequence gaps are detected, not recovered.** A hole in a link's numbering
  is reported on the message and counted. Nothing requests a retransmission,
  because that needs BATAP. The baseline is in memory, so a restart forgets
  where a link had got to rather than inventing continuity across it.
- **No multi-part split or reassembly.** 60 lines by 63 characters means a long
  passenger list arrives as `PART1`, `PART2`. Encode refuses to build an
  over-long message, which is right, but nothing splits one or joins them back.
- **No undeliverable queue for transit.** An address nothing serves is recorded
  on the message and logged; a switch parks it for an operator.
- **BATAP** is still unimplemented, so relay has no responsibility transfer:
  we forward and record, but there is no acknowledgement contract above MATIP.

## Queues

Queues exist (`internal/queue`, `store.QueueStore`) and the console shows them.
What is missing:

- **Queue numbering.** Names are Jetway's own vocabulary. A deployment that has
  to match a house convention needs a name-to-number mapping at the edge.
- **The sweeper scans.** `Sweeper.Sweep` lists records and filters in Go. That
  is fine at demo volume and wrong at scale, where the due-date predicates want
  to be an indexed query. `Limit` bounds the damage in the meantime.
- **No queue-driven robots.** Placement notifies an optional `queue.Publisher`;
  nothing ships that consumes one.
- **Waitlist clearance and schedule change** are the two producers that would
  make queues earn their keep, and neither exists yet.

## Engineering

- **The spool is not bounded.** It grows until the disk does. A depth limit that
  starts refusing rather than filling the volume would be better.
- **Spool draining is serial.** One slow message holds up the queue behind it.
- **The AIRIMP profile is thin.** It covers the elements a reservation gateway
  must act on. Expect to extend it per link.
- **Locator normalisation is strict.** Characters outside the alphabet are
  rejected rather than corrected, deliberately — see `pnr.NormaliseLocator` —
  but there is no fuzzy lookup for an agent working from a bad transcription.
- **The demo fleet lives in the shipped binary.** `internal/demo` should be
  built separately from a production `jetwayd`.

## Deliberately out of scope

Availability and inventory, fares and pricing, ticketing, departure control.
Jetway is a messaging gateway and a record store. `gateway.Responder` is the
seam where an inventory system plugs in.
