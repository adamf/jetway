# Operations

## Running

```sh
docker compose up --build              # evaluation stack
jetwayd -config /etc/jetway/jetway.yaml
```

Configuration is a YAML file; see [deploy/jetway.example.yaml](../deploy/jetway.example.yaml).
`${VAR}` is substituted from the environment. A misspelled key is a hard error
rather than a silently ignored line — an ops file gets edited under pressure and
a typo must not quietly leave a listener unauthenticated.

```sh
jetwayd -config jetway.yaml -print-config    # effective config, starts nothing
```

| Flag | Meaning |
| --- | --- |
| `-config` | Configuration file, or `JETWAY_CONFIG`. Without one, the loopback demo runs. |
| `-http`, `-store`, `-dsn` | Override the corresponding config fields |
| `-no-demo-carriers` | Do not run the simulated fleet |
| `-print-config` | Show the effective configuration and exit |

`JETWAY_LOCATOR_SECRET` must be set to a stable value in any real deployment. It
keys record locator allocation. Changing it remaps the code space, so a locator
already issued will eventually be issued again to a different booking. `jetwayd`
generates an ephemeral one and warns; treat that warning as a blocker.

## Health and readiness

| Endpoint | Behaviour |
| --- | --- |
| `/healthz` | Always 200 while the process is alive. It deliberately touches no dependency: restarting because the database blipped makes the outage worse. |
| `/readyz` | 503 when the store is unusable, so a load balancer stops sending traffic. |
| `/metrics` | Prometheus text format. |

With the spool enabled, `/readyz` going 503 does **not** mean partners are being
refused. Their messages are still accepted and fsynced; only the store is
behind. That is the intended split, and it is why the two signals differ.

## Shutdown

On SIGTERM the gateway stops accepting, waits up to 20 seconds for in-flight
work, then closes links and the HTTP server. Cutting sessions without draining
loses whatever was mid-pipeline, which on a store-and-forward link means a
partner believes a message was delivered that was never finished.

## Message states

| State | Meaning | Action |
| --- | --- | --- |
| `received` | Bytes are durable, nothing has read them | none |
| `decoded` | Envelope and body parsed | none |
| `applied` | Changed a record, or correctly required no change | none |
| `rejected` | Understood and refused — a duplicate, or test traffic | none; expected |
| `dlq` | Could not be processed | investigate, fix, replay |
| `sent` | Outbound, handed to a transport | none |
| `undeliverable` | Outbound, no transport accepted it | check the link, resend |

Nothing leaves the system on the `dlq` path. Messages wait there to be replayed.

## When a message fails

```sh
jetwayctl messages 100          # find it
jetwayctl show <message-id>     # raw bytes and decoded structure
```

The decoded view names which layer complained and why. Common causes, roughly in
order of frequency:

**Framing.** Diagnostics that make no sense at a strange offset usually mean the
framer is wrong, not the parser. Check the header width and whether the length
includes the header.

**Dialect.** Unparsed fragments on a record, or `?` lines in `jetwayctl decode`,
mean the partner sends something the profile does not recognise. The message was
still applied; add a recognizer and replay if the fragment mattered.

**Envelope arithmetic.** `unt_count_mismatch` or `control_ref_mismatch` mean a
truncated or spliced interchange. Compare the byte count against what the
partner believes they sent — this is usually a transport problem.

**A locator we do not hold.** A reply naming a record locator this system does
not have is a real divergence with the partner, and the gateway refuses to
invent a record to receive it. Reconcile before replaying.

Once fixed, replay from the stored bytes:

```sh
jetwayctl replay <message-id>
```

Replay re-runs the whole pipeline. If the message was already applied, dedup
recognises it and refuses — which is also how you can verify dedup is working.

## What to watch

| Metric | Meaning |
| --- | --- |
| `jetway_spool_depth` | Inbound messages accepted but not yet persisted. Rising means the store is behind or down. |
| `jetway_spool_oldest_seconds` | Age of the oldest unpersisted message. The number to page on. |
| `jetway_egress_retry_queue` | Messages awaiting redelivery. A depth that stops falling means a partner is unreachable. |
| `jetway_egress_abandoned_total` | Deliveries given up on. Should be zero. |
| `jetway_ingress_rejected_total` | Connections refused before any message — usually a certificate that is not mapped. |
| `jetway_ingress_refused_total` | Messages the pipeline would not accept. Should be zero. |
| `jetway_ingest_seconds` | Time to accept an inbound message. |

- **Dead letter depth.** Should be zero. Any sustained non-zero value is a
  dialect or framing problem that is still happening.
- **Unparsed fragment rate per link.** The leading indicator of dialect drift.
  It rises before anything breaks.
- **Records awaiting a reply.** `PNR.AwaitingReply()` is true while a segment
  sits at `HN`. A rising count means a partner has stopped answering.
- **Version conflict rate.** A few are normal. Many mean two writers are
  fighting over the same records.
- **Link state.** `jetwayctl status`, or the pills in the console.

## Backup and retention

The message log grows with traffic and holds personal data in the raw bytes.
Partition `message` by time and expire on your regulatory retention period.

Note the tension before you build a deletion process: the event trail is what
makes an interline dispute answerable, and a right-to-erasure request wants it
gone. Decide deliberately whether erasure redacts the payload and keeps the
event skeleton, or removes both. Neither is implemented — see
[roadmap.md](roadmap.md).

## Console

`http://<host>:8080` serves a live view: link state, traffic in both directions
with raw bytes and decoded structure, records with their itineraries and event
history, and a booking form.

It has no authentication. Do not expose it beyond a trusted network.
