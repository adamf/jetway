# State machines

The status vocabularies and their legal transitions. The segment codes are
the interline action/status vocabulary (`pkg/rescode`); everything else
derives from them.

## Segment status

A segment's status is an action code. The categories drive every decision:
requests expect replies, holdings state facts, cancellations and refusals
are dead ends, advice narrates schedule change.

```mermaid
stateDiagram-v2
    direction LR
    state "NN / SS / LL — request out" as REQ
    state "HN — requested, awaiting answer" as HN
    state "HK — holding confirmed" as HK
    state "HL — holding waitlisted" as HL
    state "UC / UN / NO — refused" as REF
    state "XX / HX — cancelled" as XX

    [*] --> REQ: Book (cold cache)
    [*] --> HK: Book (free sale)
    REQ --> HN: recorded at both ends
    HN --> HK: KK / KL / TK reply
    HN --> HL: US / UU / TL reply
    HN --> REF: UC / UN / NO reply
    HK --> XX: cancel
    HL --> XX: cancel
    HN --> XX: cancel (reply still out)
    XX --> XX: late KK ignored — a dead segment is not confirmed back to life
    XX --> XX: late NO ignored — refusals do not revive either
```

Guards worth naming, each with a regression test behind it:

- **Refusals are not live** (v0.1.6): `Recompute` derives liveness from the
  category table. `NO` is exactly as dead as `XX`; a hand-written dead list
  once omitted it and a stray refusal reopened a cancelled record.
- **Late confirmations are ignored** (v0.1.20): a `KK` landing on an `XX`
  segment — replies and cancellations cross on a store-and-forward network
  — leaves the segment dead, queues a divergence, and re-sends the
  cancellation.
- **Unknown codes stay live**: a code we cannot read is not a reason to
  cancel somebody's booking.

## Record status

```mermaid
stateDiagram-v2
    direction LR
    Open --> Ticketed: documents issued (sticky)
    Open --> Cancelled: no live segment remains
    Ticketed --> Cancelled: everything cancelled
    Cancelled --> Open: a segment is genuinely held again (KK on a live path)
```

"Live" means the category table says so: holdings, pending requests, and
confirmed or waitlisted replies. Cancellations, refusals, and the advice
codes that say deleted are not; surface (ARNK) and auxiliary placeholders
never count. Ticketing is sticky — a ticketed record stays ticketed until
nothing on it lives.

## Inbound message pipeline

```mermaid
stateDiagram-v2
    direction LR
    received --> applied: decoded, matched or created, persisted
    received --> rejected: decodes but the node refuses it (e.g. AVS with no cache)
    received --> dlq: cannot be decoded, or names a record nobody holds
    note right of dlq
        never silently dropped —
        evidence of divergence
    end note
```

Resolution order for the record a message names: our own locator first;
then, for messages that amend rather than create, the partner's locator via
the external-locator index, scoped to the sending peer — a cancellation can
arrive before the reply that would have taught the sender our locator.

## Outbound message

```mermaid
stateDiagram-v2
    direction LR
    sent --> acknowledged: CONTRL / ticket-control response
    sent --> undeliverable: no open link and no route
    undeliverable --> sent: redelivery (bounded retries, backoff)
    note right of undeliverable
        a bounded ledger may trim a message
        between capture and outcome — that
        eviction is quiet, not an error
    end note
```

## Queue item

```mermaid
stateDiagram-v2
    direction LR
    pending --> worked: a person (or sweeper) takes it
    note right of pending
        placement is deduplicated per
        (queue, record, code) while pending
    end note
```

The queues themselves say what kind of attention a record needs:
`confirmation` (a partner confirmed), `unable` (a partner could not),
`schedule-change` (a flight moved under a booking), `ticketing` (a time
limit approaches; the sweeper cancels on expiry only when asked), and
`divergence` — the queue for every case where two systems' views of one
booking are known to disagree.

## Passenger, at departure control

```mermaid
stateDiagram-v2
    direction LR
    [*] --> listed: PNL / ADL ADD
    listed --> deleted: ADL DEL
    deleted --> listed: ADL ADD (reinstated)
    listed --> accepted: check-in (seat, sequence, bags)
    [*] --> standby: go-show with no spare seat, staff travel
    standby --> accepted: seat freed at check-in close
    accepted --> boarded: gate
    accepted --> offloaded: agent, or not boarded at close
    boarded --> offloaded: agent
    listed --> noshow: flight close
    note right of accepted
        an ADL DEL here is kept as
        an alert; the PFS reports the
        passenger as GOSHO
    end note
```

The PFS categories read straight off this chart: `NOSHO` is `listed →
noshow`, `OFFLD` is anything that reached `offloaded`, `GOSHO` and `NOREC`
are go-shows that flew, `IDPAD` is staff that cleared. A passenger who was
listed, accepted and boarded is not reported at all: the list was right.

## Flight, at departure control

```mermaid
stateDiagram-v2
    direction LR
    [*] --> open: first PNL part
    open --> open: PNL parts, ADLs, acceptance
    open --> checkin_closed: close check-in (standbys clear)
    checkin_closed --> closed: close flight
    open --> closed: close flight, forced
    note right of closed
        PFS PTM PSM ETL LDM CPM built,
        loadsheet produced, nothing
        changes afterwards
    end note
```
