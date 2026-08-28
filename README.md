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

```
   agent / API                                        carrier reservation systems
        │                                                    │
        ▼                                                    │
  ┌───────────────────────────────────────────┐   Type B     │   ┌──────────┐
  │  jetwayd                                  │◄─────────────┼──►│ BA res   │
  │                                           │              │   └──────────┘
  │  capture ▸ classify ▸ decode ▸ dedupe     │   EDIFACT    │   ┌──────────┐
  │          ▸ apply ▸ respond                │◄─────────────┼──►│ AA res   │
  │                                           │              │   └──────────┘
  │  ┌────────────┐   ┌────────────────────┐  │   Type B     │   ┌──────────┐
  │  │ message log│   │ PNR store + events │  │◄─────────────┴──►│ LH res   │
  │  └────────────┘   └────────────────────┘  │                  └──────────┘
  └───────────────────────────────────────────┘
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

## Design

Four decisions shape everything else.

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
| `internal/transport` | Framing, link sessions, reconnection |

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
createdb jetway
go run ./cmd/jetwayd \
  -store postgres \
  -dsn "postgres://user@host/jetway?sslmode=disable" \
  -demo-carriers=false \
  -designator 1J -tty LONRM1J
```

The schema is embedded and applied on start; `jetwayctl schema` prints it if a
DBA would rather review it first.

Set `JETWAY_LOCATOR_SECRET` to a stable value. It keys locator allocation, and
changing it remaps the code space, which will eventually reissue a locator that
is already in use. `jetwayd` generates an ephemeral one and warns loudly if you
do not.

Carriers connect to the link server (`-link`). To run a simulated carrier as its
own process, which is the shape a real deployment has:

```sh
go run ./cmd/carriersim -carrier BA -format typeb -tty LHRRMBA -link 127.0.0.1:9100
```

## Adding a carrier

Most links need three things: a `gateway.Peer`, a framing profile, and — when
the partner's dialect differs from the shipped profile — a recognizer or segment
handler. None of that requires modifying this repository. See
[docs/adding-a-carrier.md](docs/adding-a-carrier.md).

## Provenance, and what this is not

AIRIMP and the PADIS message directories are IATA publications, sold by IATA,
and they are the normative source for message composition. This project
implements the wire syntaxes, which are stable and independently documented,
and ships a **community profile** for the message grammars above them: the
elements and segments a reservation gateway must act on, in the composition most
widely used. It is not a substitute for a carrier's implementation guide, and
the code is organised on the assumption that you will adjust it per link.

Jetway is a messaging gateway and a record store. It is deliberately **not**:

- an availability or inventory system — `gateway.Responder` is the seam where
  yours plugs in, and the bundled `Inventory` is a simulator for testing;
- a fares, pricing or ticketing engine;
- a departure control system;
- an NDC or ONE Order implementation, though nothing here precludes one.

The MATIP framing profile in `internal/transport` is a length-prefix starting
point, not a conformant RFC 2351 implementation: it does not implement the
session layer, and several carriers run non-conforming variants. Validate it
against the carrier's interface control document before putting a link into
production.

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
runs the same assertions against both backends, and the EDIFACT codec is fuzzed
for round-trip stability — a property that has already caught five real
defects. See [CONTRIBUTING.md](CONTRIBUTING.md).

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
