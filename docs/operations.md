# Operations

## Running

```sh
docker compose up --build              # evaluation stack
jetwayd -config /etc/jetway/jetway.yaml
```

Configuration is a YAML file. See [deploy/jetway.example.yaml](../deploy/jetway.example.yaml).
`jetwayd` substitutes `${VAR}` from the environment. A misspelled key is a
hard error rather than a silently ignored line. Operators edit an ops file
under pressure, and a typo must not silently leave a listener
unauthenticated.

```sh
jetwayd -config jetway.yaml -print-config    # effective config, starts nothing
```

| Flag | Meaning |
| --- | --- |
| `-config` | Configuration file, or `JETWAY_CONFIG`. Without one, the loopback demo runs. |
| `-http`, `-store`, `-dsn` | Override the corresponding config fields |
| `-no-demo-carriers` | Do not run the simulated fleet |
| `-print-config` | Show the effective configuration and exit |

Set `JETWAY_LOCATOR_SECRET` to a stable value in any production deployment.
It keys record locator allocation. Changing it remaps the code space. A
locator already issued will then eventually be issued again to a different
booking. Without the variable, `jetwayd` generates an ephemeral secret and
warns. Treat that warning as a blocker.

## Health and readiness

| Endpoint | Behaviour |
| --- | --- |
| `/healthz` | Always 200 while the process is alive. It deliberately touches no dependency. Restarting because the database blipped makes the outage worse. |
| `/readyz` | 503 when the store is unusable, so a load balancer stops sending traffic. |
| `/metrics` | Prometheus text format. |

With the spool enabled, a 503 from `/readyz` does **not** mean that partners
are refused. The gateway still accepts and fsyncs their messages. Only the
store is behind. That split is intended, and it is the reason the 2 signals
differ.

## Shutdown

On SIGTERM, the gateway stops accepting and waits up to 20 seconds for
in-flight work. It then closes the links and the HTTP server. Cutting
sessions without draining loses whatever was mid-pipeline. On a
store-and-forward link, the partner then believes that a message was
delivered when it was never finished.

## Message states

| State | Meaning | Action |
| --- | --- | --- |
| `received` | Bytes are durable, nothing has read them | none |
| `decoded` | Envelope and body parsed | none |
| `applied` | Changed a record, or correctly required no change | none |
| `rejected` | Understood and refused, such as a duplicate or test traffic | none; expected |
| `dlq` | Could not be processed | investigate, fix, replay |
| `sent` | Outbound, handed to a transport | none |
| `undeliverable` | Outbound, no transport accepted it | check the link, resend |

Nothing leaves the system on the `dlq` path. Messages wait there for replay.

## Failed messages

```sh
jetwayctl messages 100          # find it
jetwayctl show <message-id>     # raw bytes and decoded structure
```

The decoded view names the layer that reported the problem and the reason.
The common causes, roughly in order of frequency, are:

**Framing.** Diagnostics that make no sense at a strange offset usually mean
that the framer is wrong rather than the parser. Check the header width and
whether the length includes the header.

**Dialect.** Unparsed fragments on a record, or `?` lines in
`jetwayctl decode`, mean that the partner sends something the profile does
not recognise. The gateway still applied the message. Add a recognizer and
replay if the fragment mattered.

**Envelope arithmetic.** `unt_count_mismatch` or `control_ref_mismatch` mean
a truncated or spliced interchange. Compare the byte count against what the
partner believes they sent. This is usually a transport problem.

**A locator we do not hold.** A reply that names a record locator this system
does not have is a divergence with the partner. The gateway refuses to invent
a record to receive it. Reconcile before replaying.

After the fix, replay from the stored bytes:

```sh
jetwayctl replay <message-id>
```

Replay re-runs the whole pipeline. If the message was already applied, dedup
recognises it and refuses. This is also a way to verify that dedup is
working.

## Signals to watch

| Metric | Meaning |
| --- | --- |
| `jetway_spool_depth` | Inbound messages accepted but not yet persisted. Rising means the store is behind or down. |
| `jetway_spool_oldest_seconds` | Age of the oldest unpersisted message. The number to page on. |
| `jetway_egress_retry_queue` | Messages awaiting redelivery. A depth that stops falling means a partner is unreachable. |
| `jetway_egress_abandoned_total` | Deliveries given up on. Should be zero. |
| `jetway_ingress_rejected_total` | Connections refused before any message, usually a certificate that is not mapped. |
| `jetway_ingress_refused_total` | Messages the pipeline would not accept. Should be zero. |
| `jetway_ingest_seconds` | Time to accept an inbound message. |

- **Dead letter depth.** It should be zero. Any sustained non-zero value is a
  dialect or framing problem that is still happening.
- **Unparsed fragment rate per link.** This is the leading indicator of
  dialect drift. It rises before anything breaks.
- **Records awaiting a reply.** `PNR.AwaitingReply()` is true while a segment
  sits at `HN`. A rising count means a partner has stopped answering.
- **Version conflict rate.** A few conflicts are normal. Many conflicts mean
  that 2 writers are contending for the same records.
- **Link state.** Use `jetwayctl status`, or the pills in the console.

## Backup and retention

The message log grows with traffic and holds personal data in the raw bytes.
Partition `message` by time and expire it on your regulatory retention
period.

Consider the conflict before you build a deletion process. The event trail
makes an interline dispute answerable, and a right-to-erasure request
requires its removal. Decide whether erasure redacts the payload and keeps
the event skeleton, or removes both. Neither is implemented. See
[roadmap.md](roadmap.md).

## Console

`http://<host>:8080` serves a live view. It shows link state, traffic in both
directions with raw bytes and decoded structure, records with their
itineraries and event history, and a booking form.

It has no authentication. Do not expose it beyond a trusted network.

## Tracing

Tracing is off unless the config gives an endpoint for spans. Point it at any
OTLP HTTP collector:

```yaml
telemetry:
  endpoint: http://collector:4318/v1/traces
  service_name: jetway
  sample_ratio: 1
```

The spans and the context propagation are standard OpenTelemetry. Only the
exporter is written here, because OTLP permits a JSON body and every
collector accepts it. That keeps the module tree at 33 modules rather than
96. The protobuf exporter brings grpc, protobuf and grpc-gateway, the same
stack that `pkg/metrics` declined. Carriers audit this tree.

A message that arrives over HTTP carries whatever trace the caller
propagated. An agent or an NDC client that is already tracing therefore keeps
one trace across the boundary. Teletype and EDIFACT links have nowhere to put
a `traceparent`, and a message on one of them starts a new trace. In both
cases the gateway writes the trace and span identifiers against the message
in the log. A message therefore still names its trace long after the spans
have been sampled away.

**No passenger data goes in a span.** Spans leave the deployment, usually to
a collector that somebody else runs, and they outlive the intention to keep
them. Locators and carrier codes are in spans because an operator follows a
booking by them, and they are meaningless without the record. Names,
contacts, documents and frequent flyer numbers are not in spans. A test holds
the vocabulary to this rule.

### Span vocabulary

The same spans answer an operational question and a commercial one. For this
reason there is one vocabulary rather than 2 that drift.

| Span | Operations reads | The commercial side reads |
| --- | --- | --- |
| `jetway.ingest` | format, kind, status, decoder diagnostics, duplicates | which partners send what, and how much of it is unreadable |
| `jetway.send` | peer, size, delivery failures | — |
| `jetway.book` | segments, carriers | seats, interline share, **free sale share**, outcome |
| `jetway.ticket.issue` | coupons written | tickets and coupons issued |
| `jetway.emd.issue` | — | **ancillary revenue by reason-for-issuance code**, with the amount and currency |
| `jetway.cancel` | carriers unreachable | cancellations |
| `jetway.split` | carriers unadvised | divisions |

The console's **Insights** view reads the same values back out of the store.
The demo therefore shows them without a collector. The view computes them per
request, which is correct at demo volume and wrong at scale. A production
deployment reads these values from the metrics endpoint or from the traces,
which count them as they happen.
