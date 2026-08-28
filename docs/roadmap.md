# What is missing

An honest list. Things in the first two sections block a production deployment.

Recently closed: partner-facing ingress over HTTPS, TCP and file drop; peer
identity from mutual TLS; outbound retry with restart recovery; a durable
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

- **MATIP session layer.** Only the length framing is implemented, not RFC 2351
  session control. Validate against the carrier's ICD before going live.
- **IBM MQ transport.** Many carriers offer MQ rather than a socket. Ingress is
  an interface and MQ fits it; nobody has written it.
- **SITA and ARINC network access.** Reaching a carrier over Type B needs a
  commercial contract and an assigned address, not code.
- **Backpressure.** No admission control if a partner floods a link.
- **Certificate rotation.** Server and client certificates are read once at
  start. Rolling one means a restart.

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
