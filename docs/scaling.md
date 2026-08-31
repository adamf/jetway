# Scaling

Measured, not estimated. Every number below came from `go test -bench` on a
2026 laptop against a local PostgreSQL 17 with stock settings; reproduce them
with the commands in [Reproducing](#reproducing). They are a floor, not a
projection: real hardware and a tuned database do better, and the shape of the
curve matters more than the absolute figures.

## What one process does today

| Operation | Cost | Rate, single-threaded |
| --- | --- | --- |
| Type B parse | 1.0 µs | ~960,000/s |
| EDIFACT parse | 4.7 µs | ~210,000/s |
| Full inbound pipeline, in-memory store | 14 µs | ~70,000/s |
| `AppendMessage` (capture) | 73 µs | ~13,700/s |
| `GetPNR` | 39 µs | ~25,500/s |
| `UpdatePNR` with events | 221 µs | ~4,500/s |
| `CreatePNR` with events | 167 µs | ~6,000/s |

**The codecs are not the bottleneck and never will be.** Parsing is a rounding
error next to the database; a message costs about seventy times more to store
than to decode. Anybody optimising the wire formats for throughput is
optimising the wrong thing.

Capture does scale with concurrency, up to a point:

| Concurrency | Cost | Rate |
| --- | --- | --- |
| 1 | 75 µs | 13,400/s |
| 4 | 32 µs | 31,100/s |
| 12 | 25 µs | 40,100/s |
| 32 | 26 µs | 38,700/s |

It plateaus around **40,000 captures per second** at about twelve in flight,
and gets slightly worse beyond that — the classic connection-pool overshoot.

An inbound message that changes a record costs roughly **450–500 µs of database
time**: capture, read, write with events, status update, and the capture of any
reply. Call it **2,000 applied messages per second per process** against one
untuned database, with headroom to perhaps 8,000–10,000 once the database is
tuned and the pool is sized properly.

## What breaks first, and it is not throughput

Three lookups walk the record table on the hot path:

| Site | When it runs |
| --- | --- |
| `gateway.applySchedule` | every SSM or ASM |
| `gateway.findTicket` | every inbound TKCREQ/TKCRES |
| `gateway.findByExternalLocator` | every ticket advice from a validating carrier |
| `queue.Sweeper.Sweep` | every pass, by design |
| `api.insights` | every request to the console's Insights view |

The cost is exactly linear:

| Records in store | One `findTicket` |
| --- | --- |
| 100 | 24 µs |
| 1,000 | 400 µs |
| 10,000 | 4.0 ms, and 8 MB allocated |

At a million records — small for a real GDS — a single ticket control message
would cost something like 400 ms and 800 MB of garbage. That alone rules out
production.

**But the worse problem is that these are correctness bugs, not performance
bugs.** `ListPNRs` is `ORDER BY updated_at DESC LIMIT n`. The scans therefore
examined only the most recently touched records, so a ticket control message for
a booking made last month did not find it and was refused with "no record holds
this document". The partner was told something false. That failure got *more*
likely as the store grew, and it was silent.

### Fixed

The three call sites now go through `store.Lookup`, whose contract is that an
implementation searches every record or returns an error — it may never quietly
answer from a prefix. Postgres serves all three from the existing
`pnr_state_idx` GIN index by JSON containment, measured on 20,000 records:

| Lookup | Plan | Time |
| --- | --- | --- |
| by document number | Bitmap Index Scan on `pnr_state_idx` | 0.16 ms |
| by partner locator | Bitmap Index Scan on `pnr_state_idx` | 0.23 ms |
| by flight and date | BitmapOr over `pnr_state_idx` | 0.55 ms, 23 rows |

Three things were worth learning while doing it:

- **Containment over-matches, so it narrows rather than decides.** It will match
  a segment this node has already cancelled, and it ignores segment type. Both
  backends therefore filter what comes back through one shared
  `store.SegmentOnFlight`, and the Postgres lookup pages until the rows run out
  rather than trusting the first page — stopping early would report fewer
  passengers on a flight than are really on it, which is the same class of quiet
  wrong answer being removed.
- **Carriers write the same flight both zero-padded and bare**, and containment
  is exact, so each spelling has to be asked for separately. The planner ORs
  them into one bitmap, so this costs an extra index probe, not an extra scan.
- **`ScheduleScanLimit` changed meaning.** It used to bound the search; it now
  caps how many bookings one schedule message may queue, and the gateway logs a
  warning when a message hits it. Silence there would read as "these are all the
  passengers".

The schedule path was the worst of the three, and the regression test says why:
under the old scan, a flight cancellation for a booking made six months earlier
queued **zero** tasks. No error, no log line, message marked applied. The
further ahead the change, the more passengers it missed — exactly backwards,
since a schedule change months out is the normal case.

Both regression tests were confirmed to fail against the old implementation
before being kept. This repo has twice shipped tests that encoded the same guess
as the code, so a new test does not count until it has been watched to fail.

### Still open

- Push the sweeper's due-date predicates into SQL so a pass costs a range scan
  rather than a full read.
- Compute the Insights aggregate from counters rather than from the store. It
  is honest at demo volume and wrong anywhere else, and the file says so.
- Extract document number and carrier locator into real columns with btree
  indexes. Containment against the GIN index is fast enough that this is now an
  optimisation rather than a fix.

## PostgreSQL tuning

The measurements above ran against stock settings, which is why they are a
floor:

```
shared_buffers       = 128MB     work_mem            = 4MB
effective_cache_size = 4GB       max_connections     = 100
wal_buffers          = 4MB       max_wal_size        = 1GB
synchronous_commit   = on        checkpoint_timeout  = 5min
```

For a write-heavy OLTP box — which is exactly what this is, since every message
is an insert and most are followed by an update — the pgtune shape for, say, 16
vCPU and 64 GB RAM is:

```
shared_buffers                  = 16GB      # 25% of RAM
effective_cache_size            = 48GB      # 75%, a planner hint, not an allocation
maintenance_work_mem            = 2GB
work_mem                        = 32MB      # per sort node; multiply by connections before raising
wal_buffers                     = 64MB
max_wal_size                    = 16GB      # fewer, larger checkpoints
min_wal_size                    = 4GB
checkpoint_timeout              = 15min
checkpoint_completion_target    = 0.9
random_page_cost                = 1.1       # SSD; the 4.0 default assumes spinning rust
effective_io_concurrency        = 200
max_connections                 = 200       # with pooling in front; see below
default_statistics_target       = 100
```

Three that matter more than the rest here:

**`synchronous_commit`.** Leave it `on`. The whole capture-before-acknowledge
discipline exists so that a message this gateway has acknowledged is durable;
turning off synchronous commit trades exactly that guarantee for throughput and
makes the spool pointless. If commit latency is the wall, put the WAL on its own
fast device or consider `remote_write` on a replica — do not turn it off.

**Connection pooling.** The plateau above is a pool effect. Do not give each
gateway process a hundred connections; give it enough for its concurrency
(twelve to twenty-five is where this saturates) and put pgbouncer in transaction
mode in front if you run many processes. Optimistic concurrency retries make
connection count worse than it looks, because a conflict costs a second
round trip.

**Partitioning the message log.** `message` grows without bound and is
append-only with a time-ordered ULID primary key — a natural range partition by
`at`. That also makes retention a `DROP TABLE` rather than a `DELETE` that
fights vacuum. Retention does not exist yet, and is on the roadmap.

## How many instances

Work it out from the applied-message rate rather than from a rule of thumb:

```
instances = peak messages per second / 2,000     (untuned)
          = peak messages per second / 8,000     (tuned, pooled, scans fixed)
```

Two honest caveats before anybody multiplies.

**I do not know AA's message rate.** Interline reservation volume for a carrier
that size is not public, and guessing would be worse than useless. What can be
said: Type B messages are capped at 4 KB, so bandwidth is irrelevant — this is a
message-rate problem, not a bytes problem — and availability broadcasts (AVS)
usually dominate reservation traffic by an order of magnitude. AVS is also the
cheapest thing here to process, because it touches no record.

**More instances is not currently a straightforward win**, because three pieces
of state live in process memory and would diverge:

| State | Where it lives | What goes wrong with two processes |
| --- | --- | --- |
| Availability cache | `avail.Cache`, in memory | Each process has a different idea of what is sellable, so free-sale decisions differ by which one takes the message |
| Channel sequence baselines | `Gateway.seq`, in memory | Each sees half a channel's numbering and reports gaps that are not there |
| Deduplication | The store, so shared | Fine |

The availability cache is the significant one: it is the thing that decides
whether a segment sells without asking. Two processes disagreeing about it is a
correctness problem, not a cache-hit-rate problem. Fixing it means either
sharing availability through the database, pinning each carrier's AVS feed to
one process, or accepting that free sale is per-process and saying so.

## Load balancing, and why MATIP resists it

This is the part that does not work like a web service.

**MATIP sessions are stateful and long-lived.** A session is opened, carries
many messages, and is closed; the sequence numbering that lets a gap be detected
is per channel. So:

- **You cannot round-robin messages.** A load balancer that spreads packets of
  one TCP session across backends breaks the session outright, and one that
  spreads *sessions* of one channel across backends breaks gap detection,
  because each process sees a subset of the numbering and both report holes.
- **Balance whole links, not messages.** An L4 balancer with source affinity, or
  simpler and better, static assignment: each gateway process owns a set of
  peers, and the link for a given carrier always terminates on the same process.
  Peers are already a configuration concept, so this is a deployment layout
  rather than new code.
- **Failover is a reconnection, not a rebalance.** If a process dies its peers
  reconnect and MATIP re-opens the session; the partner retransmits anything
  unacknowledged. That works because capture precedes acknowledgement — nothing
  is acknowledged that is not durable. What it does *not* do is preserve the
  sequence baseline, so expect a gap report on the first message after a
  failover. That is the honest cost of keeping the baseline in memory, and it is
  documented in `checkSequence` rather than papered over.
- **BATAP would help and is not implemented.** The acknowledgement contract above
  MATIP is what would let a partner retransmit deliberately rather than by
  timeout, which is what makes failover clean rather than merely survivable.

The other transports are easier:

| Ingress | Balancing |
| --- | --- |
| MATIP / framed TCP | Pin the link. One peer, one process. |
| HTTPS with mTLS | Ordinary L7. Each request is independent; identity is the client certificate. |
| NDC over HTTP | Ordinary L7, same as any API. |
| File drop | One consumer per directory, or a lock — two processes on one directory will race. |

So the shape of a real deployment is not a homogeneous autoscaling pool. It is a
small number of processes, each owning a set of carrier links, all sharing one
database, with the HTTP surface behind a normal balancer and the teletype links
pinned. That is closer to how a message switch is actually run than to how a web
service is.

## Reproducing

```sh
initdb -D /tmp/jwpg -U postgres --auth=trust
pg_ctl -D /tmp/jwpg -o "-p 55432 -k /tmp -c listen_addresses=127.0.0.1" -l /tmp/jwpg/log start
createdb -h 127.0.0.1 -p 55432 -U postgres jetway_bench

export JETWAY_TEST_DSN="postgres://postgres@127.0.0.1:55432/jetway_bench?sslmode=disable"
go test ./pkg/gateway/ -run XXX -bench 'Ingest|Parse|FindTicket' -benchtime 2s
go test ./pkg/store/   -run XXX -bench Postgres -benchtime 2s
go test ./pkg/store/   -run XXX -bench AppendParallel -cpu 1,4,12,32 -benchtime 2s
```
