# Operations

## Running

```sh
jetwayd \
  -http 0.0.0.0:8080 \
  -link 0.0.0.0:9100 \
  -store postgres -dsn "$JETWAY_DSN" \
  -designator 1J -tty LONRM1J \
  -demo-carriers=false
```

| Flag | Meaning |
| --- | --- |
| `-http` | Console and API |
| `-link` | Carrier link server |
| `-store` | `mem` or `postgres` |
| `-dsn` | PostgreSQL DSN, or `JETWAY_DSN` |
| `-migrate` | Apply the schema on start; idempotent, default on |
| `-designator` | Our two-character company code |
| `-tty` | Our seven-character Type B address |
| `-demo-carriers` | Run the simulated fleet in-process |

`JETWAY_LOCATOR_SECRET` must be set to a stable value in any real deployment. It
keys record locator allocation. Changing it remaps the code space, so a locator
already issued will eventually be issued again to a different booking. `jetwayd`
generates an ephemeral one and warns; treat that warning as a blocker.

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
