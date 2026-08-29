# Jetway

An open-source messaging gateway for airline and GDS reservation traffic.

Jetway terminates carrier links, decodes what arrives on them, keeps passenger
name records, and answers. It speaks the two wire formats interline reservation
traffic actually uses:

- **Type B / AIRIMP** — the teletype format carried by the SITA and ARINC
  store-and-forward networks.
- **UN/EDIFACT / PADIS** — ISO 9735 interchanges carrying IATA messages such as
  `PAOREQ` and `PAORES`.

Point a carrier's stream at it and it will capture, decode, apply and reply —
and when it meets something it does not understand, it keeps that too.

A partner reaches it over HTTPS with mutual TLS, over a framed TCP circuit, or
by dropping files in a directory. Their identity comes from the certificate
they present or the circuit they arrive on, never from a name they assert.

```
   agent / API                                        carrier reservation systems
        │                                                    │
        ▼                          ingress                   │
  ┌───────────────────────────────────────────┐  https+mTLS  │   ┌──────────┐
  │  jetwayd                                  │◄─────────────┼──►│ BA res   │
  │                                           │              │   └──────────┘
  │  capture ▸ classify ▸ decode ▸ dedupe     │  tcp+mTLS    │   ┌──────────┐
  │          ▸ apply ▸ respond                │◄─────────────┼──►│ AA res   │
  │                                           │              │   └──────────┘
  │  ┌───────┐ ┌────────────┐ ┌────────────┐  │  file drop   │   ┌──────────┐
  │  │ spool │ │ message log│ │ PNR + events│ │◄─────────────┴──►│ LH res   │
  │  └───────┘ └────────────┘ └────────────┘  │                  └──────────┘
  └───────────────────────────────────────────┘
     fsync before ack           retry with backoff on the way out
```

## Try it

```sh
go run ./cmd/jetwayd          # console on http://127.0.0.1:8080
```

That starts the gateway, a link server, and three simulated carriers that
connect back over real TCP sockets — two speaking Type B, one speaking EDIFACT,
each with its own record store and its own seat inventory. Open the console,
make a booking, and watch the messages cross the links in both directions, with
the raw wire bytes and the decoded structure side by side.

Things worth trying in the console:

| Try this | What it shows |
| --- | --- |
| Book class **Z** | The carrier refuses: `UC`, and the record goes to cancelled |
| Book the same flight until seats run out | `KK` → `US` (waitlisted) → `UC` |
| Open a received message and press **Replay** | Recognised as a retransmission and refused, not booked twice |
| Compare a Type B message with an EDIFACT one | The same booking on two very different wires |

From the command line:

```sh
go run ./cmd/jetwayctl status
go run ./cmd/jetwayctl book BA 0175 Y LHR JFK
go run ./cmd/jetwayctl pnr ABC23D
go run ./cmd/jetwayctl messages
```

`jetwayctl decode` works with no server at all, which is what you want when a
partner sends something puzzling:

```sh
go run ./cmd/jetwayctl decode captured.tty
```

## Connecting a partner

Ingress is configuration, not code. Each listener declares how bytes are framed
and how the sender is identified:

```yaml
ingress:
  - name: partners-https
    type: https
    addr: 0.0.0.0:8443
    tls:
      cert: /etc/jetway/tls/server.crt
      key: /etc/jetway/tls/server.key
      client_ca: /etc/jetway/tls/partners-ca.crt   # requires a client certificate
    identify:
      by_cert_cn:
        gateway.ba.example.com: BA
    synchronous: true          # return the reply in the response body

  - name: link-lh
    type: tcp
    addr: 0.0.0.0:9103
    framing: {kind: length_prefix, header_bytes: 2, inclusive: true}
    tls: {cert: ..., key: ..., client_ca: ...}
    identify:
      by_cert_cn: {res.lh.example.com: LH}

  - name: ba-batch
    type: filedrop
    dir: /var/spool/jetway/in/ba
    stable_for: 5s             # do not read a file still being uploaded
    identify: {peer: BA}
```

A certificate signed by the right CA but not mapped to a peer is **refused**,
not fallen back to a default. That is the case that matters: the TLS handshake
succeeds, so only the mapping stands between a stranger and writing to somebody
else's records.

Replies go out over each peer's configured egress — back down the inbound
session, dialled out, posted, or dropped in a directory — and are retried with
backoff. A restart recovers the backlog from the message log rather than
trusting an in-memory queue to have survived.

Worked examples: [deploy/jetway.example.yaml](deploy/jetway.example.yaml) for a
real deployment, [deploy/jetway.compose.yaml](deploy/jetway.compose.yaml) for
the container stack. Full walkthrough in
[docs/adding-a-carrier.md](docs/adding-a-carrier.md).

## Design

Five decisions shape everything else.

**Raw bytes are made durable before anything interprets them.** Capture is the
first stage of the pipeline and it is unconditional. Every later stage is a
function of those bytes plus configuration, so a parser fix can be applied to
traffic that already failed — `POST /api/message/{id}/replay` — instead of
asking a partner to retransmit something they consider delivered. It also means
"what did we actually receive at 14:32" has an answer that does not depend on
which parser was deployed at the time.

**Nothing that cannot be understood is discarded.** An unrecognised AIRIMP line
or an unmapped EDIFACT segment becomes an unparsed fragment attached to the
record. A dialect gap then shows up as visible data on a live booking rather
than as silence. A message that cannot be decoded at all goes to a dead letter
queue with its bytes intact; it never leaves the system.

**PNR state is derived and versioned.** `pnr.state` is a projection for cheap
reads; `pnr_event` holds every change with the id of the message that caused
it. A gateway and a carrier can be modifying one record at the same instant, so
writes carry the version they read and a stale write is refused rather than
allowed to overwrite what it never saw.

**Acknowledging a partner does not depend on the database.** Ingest fsyncs the
raw bytes to a local spool and acknowledges; a drainer moves them into the store
afterwards and retries for as long as it takes. Without that, a Postgres
failover becomes refused acknowledgements and a bet on every partner's
retransmission behaviour. With it, `/readyz` goes 503 so a load balancer backs
off while partners still get a clean 202.

**Wire syntax is exact; message grammar is a profile.** ISO 9735 and the Type B
envelope are stable and universal, so those layers are strict about what they
validate. Message composition varies by carrier, version and bilateral
agreement, so the layers above are ordered recognizers and segment handlers you
replace per link — `airimp.Profile`, `padis.Profile` — without forking
anything. An unknown message type still decodes at the syntax layer, so it can
be captured, routed and replayed even when nothing above knows what it means.
See [Provenance](#provenance-and-what-this-is-not).

## Packages

| Package | What it is |
| --- | --- |
| `pkg/typeb` | Type B teletype envelope: priority and address lines, origin line, character repertoires |
| `pkg/edifact` | UN/EDIFACT ISO 9735 syntax: UNA service characters, release characters, repetitions, envelope validation |
| `pkg/airimp` | AIRIMP message grammar over Type B text, as an extensible recognizer profile |
| `pkg/padis` | IATA PADIS message mapping over EDIFACT, as an extensible segment-handler profile |
| `pkg/rescode` | The reservation action and status vocabulary both wire formats share |
| `pkg/pnr` | The canonical passenger name record, date resolution and record locator allocation |
| `internal/store` | Append-only message log and event-sourced PNR store; in-memory and Postgres |
| `internal/gateway` | The pipeline, routing, response generation and seat inventory |
| `pkg/matip` | MATIP (RFC 2351): packet format and the Type B session handshake |
| `internal/ingress` | MATIP, HTTPS, TCP and file-drop listeners, and peer identity |
| `internal/egress` | Outbound delivery with backoff and restart recovery |
| `internal/spool` | Durable write-ahead buffer for inbound messages |
| `internal/config` | Deployment configuration |
| `internal/metrics` | Prometheus exposition, no client library |
| `internal/transport` | Framing and link sessions |

The `pkg/...` tree is the part you would import to build something else. It has
no dependency on the gateway, the store, or each other beyond the canonical
model.

## Two details that bite in production

**Airline messages carry no year.** A segment says `15JUN`. Resolving that
against the wrong year silently misfiles a booking and breaks every subsequent
match against it. `pnr.ResolveDate` resolves against the time the message was
*received*, not the current time, so replaying an old message reproduces the
original reading, and it refuses `29FEB` in a non-leap year rather than
quietly shifting the departure to 1 March.

**Record locators must be unique, unguessable and cheap.** Allocating them
sequentially leaks booking volume and invites enumeration. Allocating them
randomly needs a uniqueness check and a retry loop that contends exactly when
traffic is heaviest. `pnr.LocatorAllocator` runs a keyed Feistel network over
the 32⁶ code space: a bijection, so distinct counter values always produce
distinct locators with no lookup and no retry, while the output order reveals
nothing about the input order. The alphabet omits `I`, `O`, `0` and `1`,
because locators get read aloud.

## Running it for real

```sh
docker compose up --build          # gateway + Postgres + the simulated fleet
```

or directly:

```sh
createdb jetway
export JETWAY_DSN="postgres://user@host/jetway?sslmode=disable"
export JETWAY_LOCATOR_SECRET=$(openssl rand -hex 32)
jetwayd -config /etc/jetway/jetway.yaml
```

`jetwayd -print-config` shows the effective configuration without starting
anything. The schema is embedded and applied on start; `jetwayctl schema` prints
it if a DBA would rather review it first.

`JETWAY_LOCATOR_SECRET` must be stable. It keys record locator allocation, and
changing it remaps the code space, so a locator already issued will eventually
be issued again to a different booking. `jetwayd` generates an ephemeral one and
warns; treat that warning as a blocker.

| Endpoint | Purpose |
| --- | --- |
| `/healthz` | Liveness. Never touches a dependency — restarting because the database blipped makes the outage worse. |
| `/readyz` | Readiness. 503 when the store is unusable, so a load balancer backs off. |
| `/metrics` | Prometheus. Watch `jetway_spool_depth`, `jetway_egress_retry_queue`, and `jetway_ingress_rejected_total`. |

To run a simulated carrier as its own process:

```sh
go run ./cmd/carriersim -carrier BA -format typeb -tty LHRRMBA -link 127.0.0.1:9101
```

## Adding a carrier

Most links need three things: an ingress entry saying how they are framed and
identified, a peer entry saying how to reach them, and — when their dialect
differs from the shipped profile — a recognizer or segment handler. The first
two are configuration. See [docs/adding-a-carrier.md](docs/adding-a-carrier.md).

## Independence

Jetway is an independent implementation. It is **not affiliated with, authorised
by, or endorsed by IATA, A4A, SITA or ARINC**, and no part of any IATA
publication is reproduced here. Message formats are implemented as functional
protocols; where a specification is the normative source, the code cites the
section rather than quoting it.

## Provenance, and what this is not

The two sides differ, and it matters:

- **EDIFACT is largely open.** UN/EDIFACT syntax (ISO 9735) is free from UNECE,
  and IATA publishes the [PNRGOV EDIFACT Implementation
  Guide](https://www.iata.org/contentassets/18a5fdb2dc144d619a8c10dc1472ae80/pnrgov20edifact20implementation20guide2015_1.pdf)
  openly, documenting the PADIS segment composition. `pkg/edifact` and
  `pkg/padis` are checked against those.
- **AIRIMP is not.** It is a paid IATA publication (product IATA9098, 50th
  edition) sold on quote, and it is the normative source for teletype message
  composition. `pkg/airimp` implements the elements that are stable and widely
  documented, and treats everything else as opaque.

Either way the code is organised on the assumption that you will adjust it per
link, because carrier dialects diverge from both.

Jetway is a messaging gateway and a record store. It is deliberately **not**:

- an availability or inventory system — `gateway.Responder` is the seam where
  yours plugs in, and the bundled `Inventory` is a simulator for testing;
- a fares, pricing or ticketing engine;
- a departure control system;
- an NDC or ONE Order implementation, though nothing here precludes one.

MATIP is implemented from RFC 2351 itself, which is an open IETF document, so
`pkg/matip` follows the standard rather than approximating it: the four-byte
header, the session open, open confirm and session close handshake, and Type B
data packets. Carriers do run non-conforming variants, so still check the
partner's interface control document before a link goes live.

## Security and personal data

PNRs hold passport, address and contact details. `SSR` codes `DOCS`, `DOCA`,
`DOCO` and `FOID` are flagged `Sensitive`, `PNR.Redacted()` strips them for
logs and for parties not entitled to see them, and the console redacts them.
Field-level encryption at rest and retention policy are **not yet implemented** —
see [docs/roadmap.md](docs/roadmap.md). Do not put real passenger data in a
deployment until they are.

The link handshake identifies a peer by a name it asserts. It is not
authentication, and it is not a substitute for binding identity to the
transport's own credentials. See [SECURITY.md](SECURITY.md).

## Contributing

Tests are the interesting part of this codebase: the store conformance suite
runs the same assertions against both backends, the EDIFACT codec is fuzzed for
round-trip stability — a property that has already caught six real defects —
and the ingress tests mint a throwaway certificate authority to prove that an
unmapped certificate is refused rather than accepted as a default peer. See [CONTRIBUTING.md](CONTRIBUTING.md).

```sh
make check       # format, vet, test
make test-pg     # include the Postgres store conformance tests
make fuzz        # fuzz the EDIFACT codec
```

## Documentation

- [docs/architecture.md](docs/architecture.md) — the pipeline, stage by stage
- [docs/protocols.md](docs/protocols.md) — what is implemented of each wire format
- [docs/adding-a-carrier.md](docs/adding-a-carrier.md) — onboarding a link
- [docs/operations.md](docs/operations.md) — running it, and what to do when a message fails
- [docs/roadmap.md](docs/roadmap.md) — what is missing

## Licence

MIT. See [LICENSE](LICENSE).
