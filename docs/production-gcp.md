# Running jetway in production on Google Cloud

This is the deployment a senior SRE would build for jetway carrying real
airline traffic: a carrier's reservations and departure-control host, or a
message switch, with the availability the airline business expects from
its Type B and EDIFACT plumbing. It is written against what jetway is
today (v0.1.47) and says where the software has to change before it can
be run this way. wholesky is the load generator that proves each claim
below; the numbers quoted are the ones it measured.

The short version. jetway is a stateful, connection-oriented system with a
Postgres book of record. Treat it like a database with a wire protocol,
not like a web service: regional Cloud SQL with a synchronous standby,
pinned long-lived TCP sessions behind a passthrough load balancer, one
writer per system, warm standbys that already hold the links, and a
release process that never restarts everything at once. Most of the
availability comes from the topology, not from the code.

## 1. What has to stay up, and what it costs when it does not

The service jetway provides is not "answer HTTP". It is:

- **Hold the links.** Every partner -- a GDS, an interline carrier, a
  ground handler, SITA or ARINC -- holds one or a few long-lived sessions
  to you. A link that is down is a partner queueing messages or, worse,
  routing around you. Link availability is the number they measure you by.
- **Answer inside the window.** A sell request over Type B is expected to
  be answered in seconds; the GDS's own timers run at 30 to 120 seconds
  before it queues the booking for a human. EDIFACT interactive sessions
  are tighter. Availability requests are answered in hundreds of
  milliseconds or the shopping engine drops you.
- **Never lose or duplicate a message.** The store's write-ahead spool and
  the message log exist for this. A message acknowledged and lost is a
  passenger booked at the GDS and unknown to the airline. A message applied
  twice is an oversell.
- **Keep the book of record consistent.** One record, one version, one
  writer at a time. Optimistic concurrency does the work inside one
  database; there must never be two databases that both think they are it.

Recovery objectives worth writing down before anything else: RPO zero for
the book (a committed sell is never lost), RTO under 60 seconds for a zone
loss (partners' timers), RTO under 15 minutes for a region loss with the
same RPO, and link re-establishment under 30 seconds after any failover
(the transport client's backoff caps at 15 seconds, so this holds if the
address is stable).

## 2. Topology

```
                     partners (SITA/ARINC, GDSes, carriers, GHAs)
                                  |   TCP, MATIP, TLS
                     Cloud Armor + external passthrough NLB (TCP proxy is not usable: it
                     must be L4 passthrough so source IP identification and MATIP framing survive)
                                  |
             +--------------------+--------------------+
             |    regional MIG, 3 zones, jetwayd "link" tier |   holds sessions, framing, spool,
             |    (one process per system; N systems)        |   ingest, relay; stateless except the spool
             +--------------------+--------------------+
                                  |   Cloud SQL Auth Proxy / PSC
                       Cloud SQL for PostgreSQL 17, regional HA
                       (primary + synchronous standby in another zone)
                       + read replica for consoles and reporting
                                  |
                       cross-region replica (DR), Cloud SQL backups, PITR
```

**Compute.** Compute Engine managed instance groups, regional, spread over
three zones, running the `jetwayd` container (Container-Optimized OS) or
the binary under systemd. Not Cloud Run, not GKE Autopilot: jetway's value
is long-lived inbound TCP sessions with source-IP identification and MATIP
framing, and it needs stable addresses, no request-scoped scaling, and
control over connection draining. GKE Standard works if the organisation
already runs it, with `hostNetwork` or an internal passthrough LB per
service and PodDisruptionBudgets that keep one link instance per system up
at all times; it buys nothing over MIGs here.

**Load balancing.** An external passthrough Network Load Balancer (regional,
IPv4 and IPv6) in front of the link tier, with **connection tracking by
5-tuple and connection persistence on unhealthy backends off** so a failed
instance's partners reconnect rather than hang, and session affinity by
client IP so a partner that opens several sessions lands on one instance
(jetway's `by_hello` and source-IP resolvers both assume a peer's sessions
share a process). The health check is TCP to the link port plus HTTP
`/healthz` for liveness and `/readyz` for readiness, which fails when the
store is unreachable and should also fail when the spool cannot flush or
the lease is not held (section 9).

**Database.** Cloud SQL for PostgreSQL 17 (Enterprise Plus for the
sub-second failover and 35-day PITR), regional HA: primary in one zone
with a synchronous standby in another. Connect through the Cloud SQL Auth
Proxy sidecar or Private Service Connect, never a public IP. A read replica
in the same region takes the consoles, `/api/messages`, and the
insights queries, which are the ones that scan. A cross-region replica is
the disaster-recovery copy (section 6).

**Networking.** A VPC with the link tier in a private subnet, Cloud NAT
for the few outbound dials (a carrier host dialling a switch), and the
partner-facing addresses reserved and static for the life of the contract:
partners whitelist you by IP and change control on their side takes weeks.
Cloud Armor in front of the NLB for the L3/L4 policy (partner CIDRs only;
everything else dropped at the edge). Private Google Access on the subnet
so Secret Manager, Cloud Logging and Artifact Registry are reachable
without NAT.

## 3. One system per process, one writer per system

jetway already models many systems in one database (`Postgres.Node`, the
`node` column, migration 0007). In production run **one `jetwayd` process
per system per instance**, not one process for all systems: a panic in one
carrier's decoder takes down one carrier. Each system gets:

- its own container and systemd unit, its own listener ports, its own
  metrics labels;
- its own spool directory on a persistent disk;
- the same database, as its own node view.

**Exactly one process may write a system's records at a time.** The store's
optimistic concurrency protects a single database from two writers racing
on one record; it does not protect against two processes both believing
they own a system's links, because a partner's messages would then be
split between them and answered twice. Run the link tier as **N+1 warm
standbys with leader election per system**: every instance in the MIG runs
every system's process, but only the holder of a lease (a row in a
`system_lease` table with `FOR UPDATE SKIP LOCKED`, or a Memorystore
lease; a database lease is simpler and in the same failure domain as the
book) opens the listeners for that system. When the leader dies, the lease
expires in a few seconds, a standby takes it and binds; partners reconnect
to the same NLB address and land on the new leader. This is `config.Lease`
since v0.1.48: the lease row lives in the book's database, the term
defaults to fifteen seconds and is renewed at five, so a failover takes
between five and fifteen seconds plus the partners' redial.

## 4. Capacity

Numbers from wholesky. One `jetwayd` switch relaying for 525 links on one
performance-2x machine (4 vCPU, 4GB) sustained 16,000 messages a second
through the departure banks of a full synthetic day, with Type B decode,
store write and relay for each. A carrier host with 259 tenants on 4GB
carried its share of 3,500 bookings a minute with Postgres books. The
record is about 2.7KB live and 2.2KB on disk with indexes; a filled
Southwest day (260,000 records) loaded in 24 seconds by `COPY`.

Real-world sizing for a large carrier's reservations host: a few hundred
messages a second average, a few thousand at the morning bank, a book of
5 to 20 million live records. That is:

- **Link tier:** 3 instances of e2-standard-8 (8 vCPU, 32GB) per region,
  one per zone, each able to carry the whole load alone. jetway is
  goroutine-per-link and allocation-light; CPU is decode and JSON, memory
  is the bounded message caches. Autoscale on nothing: the load is diurnal
  and known, and scaling changes addresses partners see.
- **Database:** db-perf-optimized-N-16 (16 vCPU, 128GB) primary with the
  synchronous standby; 500GB SSD to start (10 million records at 2.2KB with
  indexes and a 30-day partitioned message log is under 100GB, and the
  disk's IOPS scale with its size). The GIN index on `state` is the
  expensive one; it is what makes `FindPNRsByFlight` and the locator
  lookups fast, and it is roughly the size of the data.
- **Connections:** the Cloud SQL Auth Proxy in front of a `pgbouncer` in
  transaction mode per instance, `pool_max_conns` 24 per process (what
  wholesky runs), `max_connections` on the primary at 500. jetway sets
  `default_query_exec_mode=cache_describe` for transaction pooling and
  uses `COPY` inside explicit transactions, both of which work through
  pgbouncer; session-level advisory locks would not, and the code uses
  transaction-level ones.

Retention: the message log is partitioned by month (migration 0006) and
records by retirement day (0008). A nightly Cloud Scheduler job calls
`RetireBefore` (through an admin endpoint or a `jetwayctl retire`
command) with `now - grace`; the grace is the carrier's PNR purge policy,
typically 3 days after the last flight for the live book, with the
archive taken by the export in section 7 before the drop.

## 5. Availability engineering

**Zone loss.** The MIG's other two instances hold the load; the lease moves
in seconds; partners reconnect through the NLB. Cloud SQL fails over to
the synchronous standby in under a minute on Enterprise Plus, with no
committed transaction lost. During the failover jetway's writes fail; the
spool holds inbound messages and the gateway refuses acknowledgements
rather than acknowledging what it cannot store (`write-ahead spool
disabled` is a warning in the demo for exactly this reason: in production
the spool is on). Partners see a pause, not a loss.

**Instance loss.** Autohealing recreates it; the lease has already moved.
The spool directory is on a persistent disk that outlives the VM and is
reattached, or on a regional persistent disk if the spool must survive a
zone; unflushed spool entries are replayed on start.

**Database saturation.** The failure mode that actually happens. Guard it
with `statement_timeout` (5 seconds on the gateway's pool; the answer to
a sell that takes longer is a NAK the partner retries, not a hung link),
a queue depth alert on pgbouncer, and the outbox: with `transport.Outbox`
a slow database no longer stalls the read loop of every link, it turns
into `ErrCongested` on the sends that cannot leave, which the ledger
records as undeliverable and the redelivery loop retries.

**Slow partner.** Handled the same way, per link: the outbox fills, sends
to that peer fail fast, other links are unaffected. Alert on
`jetway_outbox_congested{peer}`; it is a partner problem until it is not.

**Deploys.** Rolling, one instance at a time, with `maxUnavailable=0`
and `maxSurge=1`, and a **connection drain**: mark the instance not-ready,
release its leases so standbys take the systems, wait for the outboxes to
empty and the spool to flush (`Drain`), then stop. Partners reconnect
once per deploy. Never deploy the link tier and a database migration in
the same window. The migration runner takes an advisory lock and
re-checks, so a rolling deploy where two instances race to migrate is
safe; still run migrations from a one-off job first, so a long conversion
(0008 on a populated book took eight minutes and doubled the table's disk
while it ran) does not happen inside an instance's startup timeout.

**Configuration.** `jetwayd` reads a YAML config; keep it in the container
image or a Secret Manager version pinned by the deploy, never mutable on
the instance. Peer additions (a new partner) are a config change and a
rolling restart; jetway has no hot reload of peers yet (section 9).

## 6. Disaster recovery

A cross-region Cloud SQL replica (asynchronous; replication lag is the
RPO, normally under a second) and a cold link tier in the second region:
the same MIG template at size zero, the same NLB configuration with its
own reserved addresses. Failover is a runbook, not automation, because a
region failover changes the IP addresses partners see and half of them
will need a phone call: promote the replica, scale the MIG up, repoint the
partners' second address (most Type B contracts specify a primary and an
alternate address; give partners the DR region's address as the alternate
on day one), and accept that in-flight messages at the failed region
after the last replicated transaction are lost -- which is why the spool
is regional and the RPO is stated as replication lag, not zero, for this
case.

Backups: automated daily with 35-day PITR on the primary, plus a weekly
logical export of each system's records to Cloud Storage with a retention
lock, because the airline's regulator will ask for a booking from four
years ago and a PITR window does not answer that. Test the restore
quarterly into a scratch instance and run `jetwayctl decode` and the
console against it.

## 7. Data handling

Records carry names, contacts, sometimes passports (the SSR `Sensitive`
flag). Cloud SQL with CMEK from Cloud KMS; the message log holds raw bytes
of everything that crossed the wire, which includes the same data, so the
same key. TLS on every partner link that will accept it (`ingress.TLS`)
and MATIP on the ones that will not; the link tier's private key in
Secret Manager, mounted at start. Access to the console behind IAP with
Google Groups, never the demo's open console. Audit logging on the Cloud
SQL instance and on Secret Manager access. A data retention policy that
matches the purge: records leave the live book at retirement and the
archive export at the regulatory period, and the message log's monthly
partitions drop on the same schedule.

Regional residency: keep the primary in the region the carrier's legal
entity is in. A European carrier's book does not replicate to `us-central1`
even for DR without the lawyers first.

## 8. Observability

jetway exposes `/metrics` (Prometheus) and OpenTelemetry traces
(`pkg/telemetry`). Ship both to Cloud Monitoring and Cloud Trace via the
Ops Agent's Prometheus receiver and an OTLP collector.

The alerts that matter, in order of who gets woken:

1. **Links down.** `jetway_ingress_links` below the contracted count for a
   partner for more than 60 seconds. Page.
2. **Undeliverable rate.** Messages the switch or a host could not deliver
   per minute, above a floor. Page if rising for 5 minutes.
3. **Reply latency.** p99 time from an inbound sell to its reply, above 5
   seconds. Page at 30.
4. **Dead letter queue depth.** Anything that was routed to the DLQ is a
   message a person must look at. Ticket on any; page above a rate.
5. **Spool depth and age.** Oldest unflushed entry over 10 seconds means
   the database is not keeping up. Page.
6. **Outbox congestion per peer.** Ticket; escalate to the partner.
7. **Divergence queue growth.** Records the airline and a partner disagree
   about, growing faster than agents work them. Ticket daily.
8. **Database:** replication lag, connections, `pg_stat_activity` waits,
   disk. Standard Cloud SQL alerting, plus a custom one on the size of
   `pnr_default` (rows that missed their daily partition mean a clock or a
   record with no flight; they should be near zero).

Dashboards: the switch console's own views are the operator's, but the
SRE dashboard is the stats page wholesky built -- message rates by class,
undeliverables, queue depths -- rebuilt in Cloud Monitoring from the same
metrics. Logs are slog JSON to stdout, collected by the Ops Agent, with the
trace id in every line that has one.

SLOs to publish to the business: link availability 99.95% per partner
per month (22 minutes), sell reply p99 under 5 seconds 99.9% of the time,
zero acknowledged-and-lost messages (measured by the reconciliation in
`internal/scenario`, run against production daily in read-only mode).

## 9. What jetway needs before any of this is true

In the order a team would build it. Items struck through were done the
day this document was written; the rest are open.

1. ~~A system lease.~~ `lease.enabled: true` makes a node bind its links
   only while it holds the system's row in `system_lease`, renewing at a
   third of the term; standbys poll and take over when it lapses or is
   released, and are not ready meanwhile. A holder that is asked to stop
   drains its links first and releases after (v0.1.51).
2. ~~Readiness.~~ `/readyz` fails when the store cannot be reached
   (`store.Pinger`), while standing by for a lease, and when the spool's
   oldest unflushed entry is older than `node.SpoolReadyAge` (30 s).
3. ~~Drain on SIGTERM.~~ `jetwayd` drains its ingresses -- in-flight
   handlers, then every session's outbox -- and the HTTP server on SIGTERM
   with a timeout, and releases the lease after.
4. ~~Spool on by default,~~ bounded by `spool.max_entries`, with
   `jetway_spool_depth` and `jetway_spool_oldest_seconds` for the alert.
5. ~~Retire as an operation.~~ `POST /api/admin/retire` and `jetwayctl
   retire --before`, so retention is a scheduled job. wholesky still runs it
   as an application side effect at the day's wrap, which is right for a
   simulation and wrong for a carrier.
6. ~~Hot peer reload.~~ SIGHUP re-reads the config and `Node.ReloadPeers`
   adds what is new. Removing a peer is a restart.
7. ~~Metrics for the outbox~~ (`jetway_outbox_depth{peer}`,
   `jetway_outbox_congested_total{peer}`) ~~and the inventory~~
   (`jetway_inventory_decisions_total{carrier,status}`, and per carrier
   `jetway_inventory_sold_seats`, `jetway_inventory_waitlisted_seats`,
   `jetway_inventory_full_cabins`, read at scrape). Seats left per cabin at
   the class boundary is `Inventory.Snapshot`, for revenue management to
   read over the API rather than a hundred thousand series.
8. ~~Rate limiting per peer~~ at ingress: `rate_limit` and `burst` pace
   each peer's reader so the partner's own circuit pushes back;
   `total_rate_limit` caps the ingress as a whole. A peer is paced to its
   own share before it reaches the shared bucket, so a flooding peer cannot
   take the others' share; `peers[].rate_limit` gives a peer its own.
9. **Load test as a release gate.** wholesky at warp 1 against a staging
   instance of the production topology, with the invariant suite (no
   oversell, message conservation, interline convergence) as the pass
   criterion. ~~The live half exists:~~ wholesky's `cmd/skycheck` asks a
   running world for `/invariants.json`, which federates every shard's
   inventory, and exits non-zero on an oversold cabin or an unreachable
   shard. ~~And the pipeline step:~~ wholesky's `gate` workflow boots a
   world at warp 240 on every push, flies it past the night, and fails the
   build on an oversold cabin, a silent shard, or a sky that did not move
   or sell. Conservation and convergence still run only in-process, because
   they need the wire quiet. Still open: a staging instance of the
   production topology rather than a single box.

## 10. Cost, roughly

Per region, list prices, without committed-use discounts: three
e2-standard-8 link instances about $600 a month; Cloud SQL Enterprise Plus
16 vCPU HA with a read replica about $3,500 a month; the cross-region DR
replica another $1,700; NLB, Cloud Armor, NAT, logging and monitoring a few
hundred. Call it $6,500 a month for one region with DR, which is what one
Type B circuit from a network provider used to cost per year in the era
the protocol was designed, and about a day of one GDS's segment fees for
a mid-sized carrier.
