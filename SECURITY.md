# Security

## Reporting a vulnerability

Report privately through the repository's security advisory page rather than in
a public issue. Please include what an attacker can achieve, not only that
something is wrong.

## What this system holds

Passenger name records contain personal data: names, contact details, and — in
`DOCS`, `DOCA`, `DOCO` and `FOID` special service requests — passport, address
and visa details. The message log holds the raw bytes of every message, so it
contains that data too, whatever the projection redacts.

## Known gaps

These are current limitations, not vulnerabilities to report. **Do not put real
passenger data in a deployment until they are addressed.**

- **No encryption at rest.** Sensitive fields are flagged and redacted from logs
  and the console, but stored in plaintext. No key management.
- **No retention or erasure.** Nothing expires. There is no erasure workflow.
- **No API authentication.** The API and console are unauthenticated. The
  console can create bookings.
- **No peer authentication.** The link handshake identifies a peer by a name it
  asserts. It stands in for out-of-band identification — a leased circuit, or
  credentials on a TLS session — and is not a substitute for it.
- **No TLS on links.** Plain TCP.

## Deploying with less risk

- Bind `-http` to a trusted interface. Put authentication in front of it.
- Terminate carrier links on a private network or over mutual TLS, and bind peer
  identity to the transport's credentials rather than the handshake name.
- Set `JETWAY_LOCATOR_SECRET` to a stable, secret value. It keys record locator
  allocation; leaking it makes locators predictable, and changing it will
  eventually reissue one already in use.
- Restrict database access. `pnr.state` and `message.raw` are the sensitive
  columns.
- Record locators are unguessable by construction, but that is obscurity, not
  authorisation. Anything exposing records must check who is asking.

## Hardening already in place

- Parsers are bounded: `edifact.DefaultMaxSegments` caps segments per
  interchange, `transport.DefaultMaxFrame` caps a frame, and the API caps
  request bodies. A corrupt length header cannot become an unbounded allocation.
- Service characters from a `UNA` are validated as plausible, so a corrupted
  header fails cleanly rather than reinterpreting the interchange.
- Interchanges marked as test are refused rather than applied.
- The dependency tree is deliberately small: a Postgres driver and its
  transitive dependencies, and nothing else. Everything above the store is
  standard library. Carriers audit this.
- Decoders are fuzzed for round-trip stability in CI.
