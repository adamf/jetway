# Security

## Reporting a vulnerability

Report a vulnerability privately through the repository's security advisory
page. Do not report it in a public issue. State what an attacker can achieve, in
addition to what is wrong.

## Stored personal data

Passenger name records (PNRs) contain personal data. They contain names and
contact details. In `DOCS`, `DOCA`, `DOCO` and `FOID` special service requests,
they contain passport, address and visa details. The message log holds the raw
bytes of every message. It therefore contains that data too, whatever the
projection redacts.

## Known gaps

These are current limitations. They are not vulnerabilities to report. **Do not
put production passenger data in a deployment until these gaps are closed.**

- **There is no encryption at rest.** Jetway flags sensitive fields and redacts
  them from logs and the console. It stores them in plaintext. There is no key
  management.
- **There is no erasure.** Retention exists: the Postgres store retires
  records by day (`RetireBefore`), and the memory store prunes records by a
  policy the host supplies (`store.Pruner`). There is no erasure workflow
  for one person's data.
- **API authentication is a single bearer token.** With `http.admin_token`
  set, every request that changes the system or reads its records needs
  `Authorization: Bearer`. Status, flights, availability and health stay open.
  Without the token, the API and console are unauthenticated, and the console
  can create bookings. Set the token, or set `http.console: false` and bind
  `http.addr` to a trusted interface. There are no per-user accounts.
- **A by-hello link accepts the name that the peer asserts.** Jetway accepts a
  peer that identifies by hello without verification, unless the peer has a
  token. On a listener that the internet can reach, set `require_token: true`
  and give every peer a token. Otherwise an unknown client can take a tokenless
  peer's name. Tokens travel in the clear unless the listener has `tls`. Use
  `tls`.
- **Jetway reads certificates once at start.** Rotation of a certificate
  requires a restart.
- **The spool is unbounded.** A long store outage fills the volume.

## Deploying with less risk

- Bind `-http` to a trusted interface, set `http.admin_token`, and put a
  TLS-terminating proxy or `http.tls` in front of it.
- On any listener that untrusted clients can reach, set `require_token`,
  `idle_timeout`, `max_connections`, `rate_limit` and `total_rate_limit`. With
  `idle_timeout`, the listener closes a quiet link. Every setting has a bounded
  default except the token requirement. The token requirement changes who may
  connect, and you must turn it on yourself.
- Configure `tls.client_ca` on every partner listener and map peers with
  `identify.by_cert_cn`. Identification by `by_cidr` is weaker and is defensible
  only on a private circuit. `identify.peer` is safe only when nothing else can
  reach the port.
- Set `JETWAY_LOCATOR_SECRET` to a stable, secret value. It keys record locator
  allocation. If it leaks, locators become predictable. If it changes, Jetway
  will eventually reissue a locator that is already in use.
- Restrict database access. `pnr.state` and `message.raw` are the sensitive
  columns.
- Record locators are unguessable by construction, but unguessability is not
  authorisation. Any component that exposes records must check who is asking.

## Hardening already in place

- Peer identity comes from the client certificate, the source network, or a
  listener dedicated to a single partner. It never comes from the payload or
  from a name that the sender asserts. Jetway refuses a certificate that the
  configured CA signed but that is not mapped to a peer. It does not treat that
  certificate as a default.
- Jetway refuses a message that the pipeline will not accept, and the sender
  sees the refusal. Over HTTP the refusal is a 503. On a socket it is a closed
  link. For a file drop, the file is left in place. The partner therefore
  retransmits instead of assuming delivery.
- Parsers are bounded. `edifact.DefaultMaxSegments` caps the segments per
  interchange, `transport.DefaultMaxFrame` caps a frame, and the API caps
  request bodies. A corrupt length header cannot cause an unbounded allocation.
- Jetway validates the service characters from a `UNA` as plausible. A
  corrupted header therefore fails cleanly, and Jetway does not reinterpret the
  interchange.
- Jetway refuses interchanges marked as test. It does not apply them.
- The dependency tree is deliberately small. It holds a Postgres driver, a YAML
  parser for the configuration file, and their transitive dependencies. Jetway
  links nothing else. The codecs, the pipeline, the transports and the metrics
  endpoint use the standard library only. Carriers audit this.
- CI fuzzes the decoders for round-trip stability.
