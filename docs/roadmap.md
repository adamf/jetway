# What is missing

An honest list. Things in the first two sections block a production deployment.

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

- **Peer authentication.** The link handshake takes a peer at its word. Identity
  must be bound to the transport's credentials — mutual TLS, or the leased
  circuit itself.
- **TLS on links.** Plain TCP today.
- **MATIP session layer.** Only the length framing is implemented, not RFC 2351
  session control. Validate against the carrier's ICD.
- **IBM MQ transport.** Many carriers offer MQ rather than a socket. The
  `Transport` interface accommodates it; nobody has written it.
- **Outbound retry and store-and-forward.** An undeliverable message is recorded
  and left. There is no retry schedule and no queue that survives a restart.
- **Backpressure.** No admission control if a partner floods a link.

## Protocol coverage

- `PNRGOV`, `PAXLST`, `TKCREQ`/`TKCRES`, `DCQCKI`/`DCRCKI` decode at the syntax
  layer and route, but do not map onto a record.
- **`CONTRL`** functional acknowledgements are neither sent nor consumed.
- **Schedule messages** (`SSM`/`ASM`) and **availability** (`AVS`) are not
  implemented.
- **`PNL`/`ADL`** passenger lists to departure control are not implemented.
- **Split and divide.** `StatusSplit` exists in the model; the operation does
  not.
- **Queues.** Real systems place records on office queues for action. There is
  no queue.
- **Timeout sweep.** Nothing notices a segment that has sat at `HN` for a day.
- **NDC and ONE Order.** Out of scope today; the architecture does not preclude
  them.

## Engineering

- **Pre-database durability.** Capture writes straight to the store. If the
  database is unavailable the transport declines to acknowledge and the peer
  retransmits, which is correct but leans on the partner. A local fsync'd spool
  drained into the store would not.
- **Metrics.** Structured logs and the event bus only. No Prometheus endpoint.
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
