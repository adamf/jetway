# Scaling

Every number in this document is measured. The numbers came from
`go test -bench` on a 2026 laptop against a local PostgreSQL 17 with stock
settings. Reproduce them with the commands in [Reproducing](#reproducing).
The numbers are a floor. Production hardware and a tuned database do better,
and the shape of the curve matters more than the absolute figures.

## Single-process performance

| Operation | Cost | Rate, single-threaded |
| --- | --- | --- |
| Type B parse | 1.0 µs | ~960,000/s |
| EDIFACT parse | 4.7 µs | ~210,000/s |
| Full inbound pipeline, in-memory store | 14 µs | ~70,000/s |
| `AppendMessage` (capture) | 73 µs | ~13,700/s |
| `GetPNR` | 39 µs | ~25,500/s |
| `UpdatePNR` with events | 221 µs | ~4,500/s |
| `CreatePNR` with events | 167 µs | ~6,000/s |

**The codecs are not the bottleneck and never will be.** Parsing costs little
next to the database. A message costs about 70 times more to store than to
decode. Optimising the wire formats for throughput targets the wrong cost.

Capture scales with concurrency up to a limit:

| Concurrency | Cost | Rate |
| --- | --- | --- |
| 1 | 75 µs | 13,400/s |
| 4 | 32 µs | 31,100/s |
| 12 | 25 µs | 40,100/s |
| 32 | 26 µs | 38,700/s |

Capture plateaus around **40,000 captures per second** at about 12 in
flight. Beyond that, it gets slightly worse. This is connection-pool
overshoot.

An inbound message that changes a record costs roughly **450–500 µs of
database time**. That time covers capture, read, write with events, status
update, and the capture of any reply. The rate is about **2,000 applied
messages per second per process** against one untuned database. With a tuned
database and a correctly sized pool, the rate can reach perhaps
8,000–10,000.

## Hot-path record scans

On the hot path, 3 lookups walk the record table:

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

A million records is small for a GDS. At that size, a single ticket control
message would cost about 400 ms and 800 MB of garbage. That cost alone rules
out production.

**The worse problem is that these are correctness bugs before they are
performance bugs.** `ListPNRs` is `ORDER BY updated_at DESC LIMIT n`. The
scans therefore examined only the most recently touched records. A ticket
control message for a booking made last month did not find the booking. The
gateway refused it with "no record holds this document" and told the partner
something false. That failure became *more* likely as the store grew, and it
was silent.

### Completed fixes

The 3 call sites now go through `store.Lookup`. Its contract is that an
implementation searches every record or returns an error. It may never answer
silently from a prefix. Postgres serves all 3 lookups from the existing
`pnr_state_idx` GIN index by JSON containment. The table gives measurements
on 20,000 records:

| Lookup | Plan | Time |
| --- | --- | --- |
| by document number | Bitmap Index Scan on `pnr_state_idx` | 0.16 ms |
| by partner locator | Bitmap Index Scan on `pnr_state_idx` | 0.23 ms |
| by flight and date | BitmapOr over `pnr_state_idx` | 0.55 ms, 23 rows |

The work produced 3 lessons:

- **Containment over-matches, and it narrows rather than decides.** It
  matches a segment this node has already cancelled, and it ignores segment
  type. Both backends therefore filter the results through one shared
  `store.SegmentOnFlight`. The Postgres lookup pages until the rows run out.
  It does not trust the first page. Stopping early would report fewer
  passengers on a flight than are on it. That is the same class of silent
  wrong answer that this work removes.
- **Carriers write the same flight both zero-padded and bare.** Containment
  is exact. The lookup therefore queries each spelling separately. The
  planner ORs the 2 queries into one bitmap. This costs an extra index probe
  and not an extra scan.
- **`ScheduleScanLimit` changed meaning.** It used to bound the search. It
  now caps how many bookings one schedule message may queue. The gateway logs
  a warning when a message hits the cap. Silence there would read as "these
  are all the passengers".

The schedule path was the worst of the 3, and the regression test shows why.
Under the old scan, a flight cancellation for a booking made 6 months earlier
queued **zero** tasks. There was no error and no log line, and the message
was marked applied. The further ahead the change, the more passengers the
scan missed. That is backwards, because a schedule change months out is the
normal case.

Both regression tests failed against the old implementation before they were
kept. This repository has twice shipped tests that encoded the same guess as
the code. A new test therefore does not count until it has been watched to
fail.

### Open items

- Push the sweeper's due-date predicates into SQL. A pass then costs a range
  scan rather than a full read.
- Compute the Insights aggregate from counters rather than from the store.
  The aggregate is correct at demo volume and wrong anywhere else, and the
  file says so.
- Extract document number and carrier locator into dedicated columns with
  btree indexes. Containment against the GIN index is fast enough that this
  is now an optimisation rather than a fix.

## PostgreSQL tuning

The measurements above ran against stock settings. For this reason they are
a floor:

```
shared_buffers       = 128MB     work_mem            = 4MB
effective_cache_size = 4GB       max_connections     = 100
wal_buffers          = 4MB       max_wal_size        = 1GB
synchronous_commit   = on        checkpoint_timeout  = 5min
```

jetway is a write-heavy online transaction processing (OLTP) workload,
because every message is an insert and most are followed by an update. For
such a workload, the pgtune shape for 16 vCPU and 64 GB RAM is:

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

These 3 settings matter more than the rest:

**`synchronous_commit`.** Leave it `on`. The capture-before-acknowledge
discipline exists to make every message this gateway has acknowledged
durable. Turning off synchronous commit trades that guarantee for throughput
and makes the spool pointless. If commit latency is the limit, put the
write-ahead log (WAL) on its own fast device or consider `remote_write` on a
replica. Do not turn `synchronous_commit` off.

**Connection pooling.** The plateau above is a pool effect. Do not give each
gateway process 100 connections. Give it enough connections for its
concurrency, which saturates at 12 to 25. Put pgbouncer in transaction mode
in front if you run many processes. Optimistic concurrency retries make the
connection count worse than it looks, because a conflict costs a second
round trip.

**Partitioning the message log.** `message` grows without bound. It is
append-only with a time-ordered ULID primary key, which suits a range
partition by `at`. Partitioning also makes retention a `DROP TABLE` rather
than a `DELETE` that fights vacuum. Retention exists in 2 forms: the
Postgres store retires records by day (`RetireBefore`, as partitions), and
the memory store prunes records by a policy the host supplies
(`store.Pruner`, v0.1.94). The message log has a size cap in memory and a
time-based purge on both stores.

## Instance count

Calculate the instance count from the applied-message rate rather than from
a rule of thumb:

```
instances = peak messages per second / 2,000     (untuned)
          = peak messages per second / 8,000     (tuned, pooled, scans fixed)
```

Apply 2 caveats before you multiply.

**AA's message rate is unknown.** Interline reservation volume for a carrier
that size is not public, and a guess would be worse than useless. Type B
messages are capped at 4 KB, and bandwidth is therefore irrelevant. The
problem is the message rate and not the byte count. Availability broadcasts
(AVS) usually dominate reservation traffic by an order of magnitude. AVS is
also the cheapest message here to process, because it touches no record.

**More instances are not currently a straightforward win**, because 3 pieces
of state live in process memory and would diverge:

| State | Where it lives | What goes wrong with two processes |
| --- | --- | --- |
| Availability cache | `avail.Cache`, in memory | Each process has a different idea of what is sellable, so free-sale decisions differ by which one takes the message |
| Channel sequence baselines | `Gateway.seq`, in memory | Each sees half a channel's numbering and reports gaps that are not there |
| Deduplication | The store, so shared | Fine |

The availability cache is the significant one. It decides whether a segment
sells without a request to the carrier. 2 processes that disagree about it
are a correctness problem and not a cache-hit-rate problem. There are
3 possible fixes. The first shares availability through the database. The
second pins each carrier's AVS feed to one process. The third accepts that
free sale is per-process and documents it.

## Load balancing and MATIP

This part does not work like a web service.

**MATIP sessions are stateful and long-lived.** A session opens, carries many
messages, and closes. The sequence numbering that lets the gateway detect a
gap is per channel. The consequences are:

- **You cannot round-robin messages.** A load balancer that spreads packets
  of one TCP session across backends breaks the session. A load balancer that
  spreads *sessions* of one channel across backends breaks gap detection.
  Each process then sees a subset of the numbering, and both report holes.
- **Balance whole links rather than messages.** Use an L4 balancer with
  source affinity, or the simpler and better option of static assignment.
  With static assignment, each gateway process owns a set of peers, and the
  link for a given carrier always terminates on the same process. Peers are
  already a configuration concept. This is therefore a deployment layout
  rather than new code.
- **Failover is a reconnection rather than a rebalance.** If a process dies,
  its peers reconnect, MATIP re-opens the session, and the partner
  retransmits anything unacknowledged. That works because capture precedes
  acknowledgement. The gateway acknowledges nothing that is not durable.
  Failover does *not* preserve the sequence baseline. Expect a gap report on
  the first message after a failover. That is the cost of keeping the
  baseline in memory, and `checkSequence` documents it.
- **BATAP would help and is not implemented.** BATAP is the acknowledgement
  contract above MATIP. It would let a partner retransmit deliberately rather
  than by timeout. That would make failover clean rather than merely
  survivable.

The other transports are easier:

| Ingress | Balancing |
| --- | --- |
| MATIP / framed TCP | Pin the link. One peer, one process. |
| HTTPS with mTLS | Ordinary L7. Each request is independent; identity is the client certificate. |
| NDC over HTTP | Ordinary L7, same as any API. |
| File drop | One consumer per directory, or a lock. Two processes on one directory will race. |

A production deployment is therefore not a homogeneous autoscaling pool. It
is a small number of processes, each owning a set of carrier links, all
sharing one database. The HTTP surface sits behind a normal balancer and the
teletype links are pinned. That is closer to how operators run a message
switch than to how they run a web service.

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
