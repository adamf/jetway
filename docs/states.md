# State machines

This document lists the status vocabularies and their legal transitions. The
segment codes are the interline action/status vocabulary (`pkg/rescode`).
Every other status derives from them.

## Segment status

A segment's status is an action code. The code categories drive every
decision. A request expects a reply. A holding states a fact. A cancellation
or a refusal is a terminal state. An advice code describes a schedule change.

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

These guards each have a regression test:

- **Refusals are not live** (v0.1.6). `Recompute` derives liveness from the
  category table. `NO` is as dead as `XX`. A hand-written dead list once
  omitted `NO`, and a stray refusal reopened a cancelled record.
- **Late confirmations are ignored** (v0.1.20). Replies and cancellations
  cross on a store-and-forward network. A `KK` that lands on an `XX` segment
  leaves the segment dead, queues a divergence, and re-sends the
  cancellation.
- **Unknown codes stay live.** A code that we cannot read is not a reason to
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

A segment is live when the category table says so. The live categories are
holdings, pending requests, and confirmed or waitlisted replies.
Cancellations, refusals, and the advice codes that mean deleted are not live.
Surface (ARNK) and auxiliary placeholders never count. Ticketing is sticky. A
ticketed record stays ticketed until no segment on it is live.

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

The pipeline resolves the record that a message names in this order. It tries
our own locator first. Then, for messages that amend an existing record, it
tries the partner's locator through the external-locator index, scoped to the
sending peer. The second step exists because a cancellation can arrive before
the reply that would have given the sender our locator.

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

The queue name states what kind of attention a record needs. `confirmation`
means a partner confirmed. `unable` means a partner could not. `schedule-change`
means a flight moved under a booking. `ticketing` means a time limit
approaches, and on that queue the sweeper cancels on expiry only when asked.
`divergence` is the queue for every case where 2 systems' views of one booking
are known to disagree.

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

The PFS categories map directly onto this chart. `NOSHO` is `listed →
noshow`. `OFFLD` is any passenger who reached `offloaded`. `GOSHO` and
`NOREC` are go-shows who flew. `IDPAD` is staff who cleared. A passenger who
was listed, accepted and boarded is not reported, because the list was right.

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
