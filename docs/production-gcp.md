# Running jetway in production on Google Cloud

This document describes the deployment a senior site reliability engineer
(SRE) would build for jetway carrying live airline traffic. The deployment is
a carrier's reservations and departure-control host, or a message switch. It
provides the availability that the airline business expects from its Type B
and EDIFACT links. The document describes jetway as it is today (v0.1.47). It
states where the software must change before jetway can run this way.
wholesky is the load generator that tests each claim below. The numbers
quoted are the numbers wholesky measured.

jetway is a stateful, connection-oriented system with a Postgres
book of record. Operate it as a database with a wire protocol. Do not operate
it as a web service.

The deployment uses regional Cloud SQL with a synchronous standby. It pins
long-lived TCP sessions behind a passthrough load balancer. It runs one
writer per system and warm standbys that already hold the links. The release
process never restarts every instance at once. Most of the availability comes
from the topology rather than from the code.

## 1. Service obligations and recovery objectives

The service that jetway provides is not an HTTP service. It has 4 parts:

- **Hold the links.** Every partner holds one or a few long-lived sessions to
  you. Partners are global distribution systems (GDS), interline carriers,
  ground handlers, SITA and ARINC. When a link is down, the partner queues
  messages. In the worse case, the partner routes around you. Partners
  measure you by link availability.
- **Answer inside the window.** A partner expects a reply to a Type B sell
  request within seconds. The GDS timers run at 30 to 120 seconds before the
  GDS queues the booking for a human. EDIFACT interactive sessions have
  tighter windows. A shopping engine expects a reply to an availability
  request within hundreds of milliseconds. If the reply is later, the
  shopping engine drops you.
- **Never lose or duplicate a message.** The store's write-ahead spool and
  the message log exist for this purpose. When jetway acknowledges a message
  and then loses it, the GDS holds a booking that the airline does not know.
  When jetway applies a message twice, the result is an oversell.
- **Keep the book of record consistent.** Each record has one version and
  one writer at a time. Optimistic concurrency enforces this inside one
  database. There must never be 2 databases that each act as the book of
  record.

Define the recovery objectives first. The recovery point objective (RPO) for
the book is zero. A committed sell is never lost. The recovery time objective
(RTO) for a zone loss is under 60 seconds, because of the partners' timers.
The RTO for a region loss is under 15 minutes with the same RPO.

Links re-establish within 30 seconds after any failover. The transport
client's backoff caps at 15 seconds. The objective therefore holds if the
address is stable.

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

**Compute.** The link tier is a regional Compute Engine managed instance
group (MIG) spread over 3 zones. Each instance runs the `jetwayd` container
(Container-Optimized OS) or the binary under systemd. Cloud Run and GKE
Autopilot are not suitable, because jetway's value is long-lived inbound TCP
sessions with source-IP identification and MATIP framing. jetway needs stable
addresses, no request-scoped scaling, and control over connection draining.
GKE Standard works if the organisation already runs it, with `hostNetwork` or
an internal passthrough load balancer per service. It also needs
PodDisruptionBudgets that keep one link instance per system up at all times,
and it has no advantage over MIGs here.

**Load balancing.** A regional external passthrough Network Load Balancer
(NLB) with IPv4 and IPv6 fronts the link tier. With **connection tracking by
5-tuple and connection persistence on unhealthy backends off**, a failed
instance's partners reconnect instead of hanging. Session affinity by client
IP keeps a partner's several sessions on one instance. jetway's `by_hello`
and source-IP resolvers both assume that a peer's sessions share a process.
The health check is TCP to the link port plus HTTP `/healthz` for liveness
and `/readyz` for readiness. `/readyz` fails when the store is unreachable,
and it should also fail when the spool cannot flush or the lease is not held
(section 9).

**Database.** The database is Cloud SQL for PostgreSQL 17 with regional high
availability (HA). Use Enterprise Plus for the sub-second failover and the
35-day point-in-time recovery (PITR). The primary is in one zone with a
synchronous standby in another zone. Connect through the Cloud SQL Auth Proxy
sidecar or Private Service Connect, never through a public IP. A read replica
in the same region serves the consoles, `/api/messages`, and the insights
queries, which are the queries that scan. A cross-region replica is the
disaster-recovery copy (section 6).

**Networking.** The link tier is in a private subnet of a virtual private
cloud (VPC). Cloud NAT serves the few outbound dials, such as a carrier host
that dials a switch. Reserve the partner-facing addresses and keep them
static for the life of the contract. Partners whitelist you by IP, and change
control on their side takes weeks. Cloud Armor in front of the NLB holds the
L3/L4 policy, which admits partner CIDR ranges only and drops everything else
at the edge. Private Google Access on the subnet makes Secret Manager, Cloud
Logging and Artifact Registry reachable without NAT.

## 3. One system per process, one writer per system

jetway already models many systems in one database (`Postgres.Node`, the
`node` column, migration 0007). In production, run **one `jetwayd` process
per system per instance**. Do not run one process for all systems. A panic in
one carrier's decoder then takes down only that carrier. Each system gets:

- its own container and systemd unit, its own listener ports, its own
  metrics labels;
- its own spool directory on a persistent disk;
- the same database, as its own node view.

**Exactly one process may write a system's records at a time.** The store's
optimistic concurrency protects a single database from 2 writers racing on
one record. It does not protect against 2 processes that both act as owner
of a system's links. A partner's messages would then be split between them
and answered twice. Run the link tier as **N+1 warm standbys with leader
election per system**. Every instance in the MIG runs every system's process.
Only the holder of a lease opens the listeners for that system.

The lease is a row in a `system_lease` table with `FOR UPDATE SKIP LOCKED`,
or a Memorystore lease. A database lease is simpler and is in the same
failure domain as the book. When the leader dies, the lease expires in a few
seconds. A standby takes the lease and binds. Partners reconnect to the same
NLB address and land on the new leader.

This is `config.Lease` since v0.1.48. The lease row lives in the book's
database. The term defaults to 15 seconds and the holder renews it at
5 seconds. A failover therefore takes between 5 and 15 seconds plus the
partners' redial.

## 4. Capacity

The numbers in this section come from wholesky. One `jetwayd` switch relayed
for 525 links on one performance-2x machine (4 vCPU, 4 GB). It sustained
16,000 messages a second through the departure banks of a full synthetic
day. Each message had a Type B decode, a store write and a relay.

A carrier host with 259 tenants on 4 GB carried its share of 3,500 bookings
a minute with Postgres books. The record is about 2.7 KB live and 2.2 KB on
disk with indexes. A filled Southwest day (260,000 records) loaded in
24 seconds by `COPY`.

A large carrier's reservations host handles a few hundred messages a second
on average, and a few thousand at the morning bank. Its book holds 5 to
20 million live records. That load needs:

- **Link tier:** run 3 instances of e2-standard-8 (8 vCPU, 32 GB) per
  region, one per zone. Each instance can carry the whole load alone. jetway
  runs one goroutine per link and allocates little. CPU goes to decode and
  JSON, and memory goes to the bounded message caches. Do not autoscale. The
  load is diurnal and known, and scaling changes the addresses that partners
  see.
- **Database:** run a db-perf-optimized-N-16 (16 vCPU, 128 GB) primary with
  the synchronous standby. Start with 500 GB of SSD. A book of 10 million
  records at 2.2 KB with indexes, plus a 30-day partitioned message log, is
  under 100 GB. The disk's IOPS scale with its size. The GIN index on `state`
  is the expensive one. It makes `FindPNRsByFlight` and the locator lookups
  fast, and it is roughly the size of the data.
- **Connections:** run the Cloud SQL Auth Proxy in front of a `pgbouncer` in
  transaction mode on each instance. Set `pool_max_conns` to 24 per process,
  which is what wholesky runs. Set `max_connections` on the primary to 500.
  jetway sets `default_query_exec_mode=cache_describe` for transaction
  pooling and uses `COPY` inside explicit transactions. Both work through
  pgbouncer. Session-level advisory locks would not work through pgbouncer,
  and the code uses transaction-level locks.

The message log is partitioned by month (migration 0006) and records by
retirement day (0008). A nightly Cloud Scheduler job calls `RetireBefore`
with `now - grace`, through an admin endpoint or a `jetwayctl retire`
command. The grace is the carrier's passenger name record (PNR) purge
policy. For the live book, this is typically 3 days after the last flight.
The export in section 7 takes the archive before the drop.

## 5. Availability engineering

**Zone loss.** The MIG's other 2 instances hold the load. The lease moves in
seconds, and partners reconnect through the NLB. Cloud SQL fails over to the
synchronous standby in under a minute on Enterprise Plus, with no committed
transaction lost.

During the failover, jetway's writes fail and the spool holds inbound
messages. The gateway refuses acknowledgements for messages it cannot store.
For this reason, the demo warns `write-ahead spool disabled`. In production
the spool is on. Partners see a pause and no loss.

**Instance loss.** Autohealing recreates the instance. The lease has already
moved. The spool directory is on a persistent disk that outlives the VM and
is reattached. Use a regional persistent disk if the spool must survive a
zone loss. The process replays unflushed spool entries on start.

**Database saturation.** This failure mode occurs in practice. Guard against
it with `statement_timeout`, a queue depth alert on pgbouncer, and the
outbox. Set `statement_timeout` to 5 seconds on the gateway's pool. A sell
that takes longer then gets a NAK that the partner retries, instead of a hung
link. With `transport.Outbox`, a slow database no longer stalls the read loop
of every link. It turns into `ErrCongested` on the sends that cannot leave,
which the ledger records as undeliverable and the redelivery loop retries.

**Slow partner.** The outbox handles a slow partner the same way, per link.
The outbox fills and sends to that peer fail fast. Other links are
unaffected. Alert on `jetway_outbox_congested{peer}`. Treat it as a partner
problem until evidence shows otherwise.

**Deploys.** Deploy in a rolling manner, one instance at a time, with
`maxUnavailable=0` and `maxSurge=1`, and a **connection drain**. The drain
marks the instance not-ready and releases its leases, and standbys take the
systems. It then waits for the outboxes to empty and the spool to flush
(`Drain`), and stops the instance. Partners reconnect once per deploy. Never
deploy the link tier and a database migration in the same window.

The migration runner takes an advisory lock and re-checks. A rolling deploy
in which 2 instances race to migrate is therefore safe. Still run migrations
from a one-off job first. A long conversion must not happen inside an
instance's startup timeout. Migration 0008 on a populated book took
8 minutes and doubled the table's disk while it ran.

**Configuration.** `jetwayd` reads a YAML config. Keep the config in the
container image or in a Secret Manager version pinned by the deploy. Never
keep it mutable on the instance. Adding a peer, such as a new partner, is a
config change. `Node.ReloadPeers` (v0.1.90) adds and dials new peers at
run time, and `SetPeerToken` changes a peer's secret, so a restart is only
needed for a change to the listeners.

## 6. Disaster recovery

Disaster recovery (DR) uses a cross-region Cloud SQL replica and a cold link
tier in the second region. The replica is asynchronous. Its replication lag
is the RPO, normally under a second. The cold link tier is the same MIG
template at size zero and the same NLB configuration with its own reserved
addresses.

Failover is a runbook. It is not automated. A region failover changes the IP
addresses that partners see, and half of the partners will need a phone
call. The runbook promotes the replica, scales the MIG up, and repoints the
partners' second address. Most Type B contracts specify a primary and an
alternate address. Give partners the DR region's address as the alternate on
day one.

In-flight messages at the failed region after the last replicated
transaction are lost. For this reason the spool is regional, and the RPO for
this case is stated as replication lag and not as zero.

Backups are automated daily with 35-day PITR on the primary. In addition, a
weekly logical export writes each system's records to Cloud Storage with a
retention lock. The airline's regulator will ask for a booking from 4 years
ago, and a PITR window does not answer that request. Test the restore
quarterly into a scratch instance. Run `jetwayctl decode` and the console
against the restored instance.

## 7. Data handling

Records carry names, contacts and sometimes passports, which the special
service request (SSR) `Sensitive` flag marks. Use Cloud SQL with
customer-managed encryption keys (CMEK) from Cloud KMS. The message log holds
the raw bytes of everything that crossed the wire, which includes the same
data. Use the same key for the message log. Use TLS on every partner link
that accepts it (`ingress.TLS`) and MATIP on the links that do not. Keep the
link tier's private key in Secret Manager and mount it at start.

Put access to the console behind Identity-Aware Proxy (IAP) with Google
Groups. Never use the demo's open console. Turn on audit logging on the
Cloud SQL instance and on Secret Manager access. Set a data retention policy
that matches the purge. Records leave the live book at retirement and leave
the archive export at the regulatory period. The message log's monthly
partitions drop on the same schedule.

For regional residency, keep the primary in the region of the carrier's
legal entity. A European carrier's book does not replicate to `us-central1`,
even for DR, without legal approval first.

## 8. Observability

jetway exposes `/metrics` (Prometheus) and OpenTelemetry traces
(`pkg/telemetry`). Ship both to Cloud Monitoring and Cloud Trace through the
Ops Agent's Prometheus receiver and an OTLP collector.

This list gives the important alerts in order of severity:

1. **Links down.** `jetway_ingress_links` is below the contracted count for
   a partner for more than 60 seconds. Page.
2. **Undeliverable rate.** The number of messages per minute that the switch
   or a host could not deliver is above a floor. Page if the rate rises for
   5 minutes.
3. **Reply latency.** The p99 time from an inbound sell to its reply is
   above 5 seconds. Page at 30 seconds.
4. **Dead letter queue depth.** Every message routed to the dead letter
   queue (DLQ) needs a person to look at it. Ticket on any message. Page
   above a rate.
5. **Spool depth and age.** An oldest unflushed entry over 10 seconds means
   that the database is not keeping up. Page.
6. **Outbox congestion per peer.** Ticket, and escalate to the partner.
7. **Divergence queue growth.** The records that the airline and a partner
   disagree about grow faster than agents work them. Ticket daily.
8. **Database:** alert on replication lag, connections, `pg_stat_activity`
   waits and disk. Use standard Cloud SQL alerting, plus a custom alert on
   the size of `pnr_default`. Rows in `pnr_default` missed their daily
   partition, which means a clock problem or a record with no flight. They
   should be near zero.

The operator uses the switch console's own views as dashboards. The SRE
dashboard is the stats page that wholesky built, rebuilt in Cloud Monitoring
from the same metrics. It shows message rates by class, undeliverables and
queue depths. Logs are slog JSON to stdout. The Ops Agent collects them, and
every line that has a trace id carries it.

Publish these service level objectives (SLO) to the business. Link
availability is 99.95% per partner per month (22 minutes). The sell reply
p99 is under 5 seconds 99.9% of the time. Acknowledged-and-lost messages are
zero, as measured by the reconciliation in `internal/scenario`, run against
production daily in read-only mode.

## 9. Prerequisites in jetway

The items are in the order a team would build them. Struck-through items
were done on the day this document was written. The rest are open.

1. ~~A system lease.~~ With `lease.enabled: true`, a node binds its links
   only while it holds the system's row in `system_lease`. The node renews
   the lease at a third of the term. Standbys poll and take over when the
   lease lapses or is released. Standbys are not ready meanwhile. A holder
   that is asked to stop drains its links first and releases the lease after
   (v0.1.51).
2. ~~Readiness.~~ `/readyz` fails when the node cannot reach the store
   (`store.Pinger`). It fails while the node stands by for a lease. It fails
   when the spool's oldest unflushed entry is older than
   `node.SpoolReadyAge` (30 s).
3. ~~Drain on SIGTERM.~~ On SIGTERM, `jetwayd` drains its ingresses and the
   HTTP server with a timeout. It drains the in-flight handlers first, then
   every session's outbox. It releases the lease after the drain.
4. ~~Spool on by default,~~ bounded by `spool.max_entries`. The metrics
   `jetway_spool_depth` and `jetway_spool_oldest_seconds` serve the alert.
5. ~~Retire as an operation.~~ `POST /api/admin/retire` and `jetwayctl
   retire --before` make retention a scheduled job. wholesky still runs
   retirement as an application side effect at the day's wrap. That is right
   for a simulation and wrong for a carrier.
6. ~~Hot peer reload.~~ On SIGHUP, `jetwayd` re-reads the config and
   `Node.ReloadPeers` adds the new peers. Removing a peer needs a restart.
7. ~~Metrics for the outbox~~ (`jetway_outbox_depth{peer}`,
   `jetway_outbox_congested_total{peer}`) ~~and the inventory~~
   (`jetway_inventory_decisions_total{carrier,status}`, and per carrier
   `jetway_inventory_sold_seats`, `jetway_inventory_waitlisted_seats`,
   `jetway_inventory_full_cabins`, read at scrape). `Inventory.Snapshot`
   gives the seats left per cabin at the class boundary. Revenue management
   reads it over the API instead of 100,000 series.
8. ~~Rate limiting per peer~~ at ingress. `rate_limit` and `burst` pace
   each peer's reader, and the partner's own circuit then pushes back.
   `total_rate_limit` caps the ingress as a whole. The ingress paces a peer
   to its own share before the peer reaches the shared bucket. A flooding
   peer therefore cannot take the other peers' share. `peers[].rate_limit`
   gives a peer its own limit. ~~Hardening for a listener on the internet~~
   adds `require_token`, `idle_timeout`, `max_connections`, and
   `http.admin_token` for the console (v0.1.94). `require_token` refuses
   tokenless hellos. The hardening followed a security pass over wholesky's
   public deployment.
9. **Load test as a release gate.** Run wholesky at warp 1 against a staging
   instance of the production topology. The pass criterion is the invariant
   suite, which checks no oversell, message conservation and interline
   convergence. ~~The live half exists:~~ wholesky's `cmd/skycheck` requests
   `/invariants.json` from a running world. That endpoint federates every
   shard's inventory. `skycheck` exits non-zero on an oversold cabin or an
   unreachable shard. ~~And the pipeline step:~~ wholesky's `gate` workflow
   boots a world at warp 240 on every push and flies it past the night. It
   fails the build on an oversold cabin, a silent shard, or a sky that did
   not move or sell. Conservation and convergence still run only in-process,
   because they need a quiet wire. A staging instance of the production
   topology, rather than a single box, is still open.

## 10. Estimated cost

These are list prices per region, without committed-use discounts. The
3 e2-standard-8 link instances cost about $600 a month. Cloud SQL Enterprise
Plus with 16 vCPU, HA and a read replica costs about $3,500 a month. The
cross-region DR replica costs another $1,700. NLB, Cloud Armor, NAT, logging
and monitoring cost a few hundred. The total is about $6,500 a month for one
region with DR.

One Type B circuit from a network provider used to cost that amount per year
in the era the protocol was designed. The total is also about a day of one
GDS's segment fees for a mid-sized carrier.
