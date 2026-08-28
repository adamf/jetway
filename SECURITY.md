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
  console can create bookings. Set `http.console: false` and bind `http.addr` to
  a trusted interface.
- **Certificates are read once at start.** Rotating one requires a restart.
- **The spool is unbounded.** A long store outage fills the volume.

## Deploying with less risk

- Bind `-http` to a trusted interface. Put authentication in front of it.
- Configure `tls.client_ca` on every partner listener and map peers with
  `identify.by_cert_cn`. Identifying by `by_cidr` is weaker and only defensible
  on a private circuit; `identify.peer` assumes nothing else can reach the port.
- Set `JETWAY_LOCATOR_SECRET` to a stable, secret value. It keys record locator
  allocation; leaking it makes locators predictable, and changing it will
  eventually reissue one already in use.
- Restrict database access. `pnr.state` and `message.raw` are the sensitive
  columns.
- Record locators are unguessable by construction, but that is obscurity, not
  authorisation. Anything exposing records must check who is asking.

## Hardening already in place

- Peer identity comes from the client certificate, the source network, or a
  listener dedicated to one partner — never from the payload or a name the
  sender asserts. A certificate signed by the configured CA but not mapped to a
  peer is refused rather than treated as a default.
- A message the pipeline will not accept is refused to the sender — 503 over
  HTTP, a closed link on a socket, a file left in place — so a partner
  retransmits instead of assuming delivery.
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
