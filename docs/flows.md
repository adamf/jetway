# Message flows

Who talks to whom, in what order. Participants are the roles a real network
has: a distribution system (GDS) that sells, a switch that relays, and a
carrier's reservation system that answers. On a switchless topology the GDS
and carrier talk directly; nothing in the flows changes but the middle
column disappearing.

## A Type B sell, requested

The cold-cache path: the GDS holds no availability assertion, so it asks.

```mermaid
sequenceDiagram
    participant A as Agent/API
    participant G as GDS gateway
    participant X as Switch (relay)
    participant C as Carrier

    A->>G: Book(request)
    G->>G: create PNR, segments HN
    G->>X: AIRIMP sell (NN) — QU carrier address
    X->>C: relay (address routing)
    C->>C: create PNR copy, segments HN
    C->>C: Inventory.Decide → KK / US / UC
    C->>X: AIRIMP reply (KK1, RLOC both locators)
    X->>G: relay
    G->>G: apply: HN→HK, learn carrier's locator
    Note over G,C: both stores now agree; AwaitingReply() is false
```

## A Type B sell, free sale

The warm-cache path: the carrier's AVS broadcast said the class is open, so
the GDS confirms on the spot and reports the sale it made.

```mermaid
sequenceDiagram
    participant G as GDS gateway
    participant C as Carrier

    Note over G: avail cache: Y open, 4 seats (AVS, fresh)
    G->>G: Book → segment HK immediately, cache count −1
    G->>C: AIRIMP sell (SS) — sold from availability
    C->>C: create PNR copy, commit inventory
    C->>G: reply (KK)
    G->>G: apply KK → HK (no-op upgrade), learn locator
    Note over G,C: the report is not optional: a carrier that never<br/>hears about a free sale diverges on every one
```

## An EDIFACT sell

Same conversation, different wire: PAOREQ asks, PAORES answers, CONTRL
acknowledges syntax separately from content.

```mermaid
sequenceDiagram
    participant G as GDS gateway
    participant C as Carrier (EDIFACT)

    G->>C: PAOREQ — MSG function 11, RPI NN per segment
    C-->>G: CONTRL (interchange acknowledged)
    C->>C: apply: segments HN, Inventory.Decide
    C->>G: PAORES — MSG function 22, RPI KK, RCI both locators
    G->>G: apply KK → HK
```

## Cancellation

A cancellation is an advisory, not a request: the sender has already
cancelled, and nothing in it asks the recipient to decide anything. On
EDIFACT it carries MSG function 1 — answering one as if it were a sell was
how a cancelled booking once got refused back to life (v0.1.6).

```mermaid
sequenceDiagram
    participant G as GDS gateway
    participant C as Carrier

    G->>G: Cancel: segments XX, Recompute → cancelled
    G->>C: AIRIMP XX / PAOREQ function 1 (both locators)
    C->>C: apply: segments XX, record cancelled
    Note over G,C: no reply travels back — the message counts<br/>are conserved, and the e2e test pins them
```

## Messages that cross on the network

Store-and-forward retries reorder deliveries. Two guards exist because real
volume found both resurrections (v0.1.6, v0.1.20).

```mermaid
sequenceDiagram
    participant G as GDS gateway
    participant C as Carrier

    G->>C: sell (delivery delayed — sits in the retry spool)
    G->>G: traveller cancels: XX, record cancelled
    G--xC: cancel — carrier holds nothing yet…
    Note over C: …but resolution falls back to the sender's own<br/>locator on the copy once the sell lands, so a cancel<br/>arriving first no longer dead-letters
    C->>G: KK — the reply to the delayed sell, after the cancel
    G->>G: late confirmation IGNORED: segment stays XX
    G->>C: cancellation re-sent (the reply proves seats are still held)
    G->>G: divergence queued for a person
```

## Availability

```mermaid
sequenceDiagram
    participant C as Carrier
    participant X as Switch
    participant G as Every GDS

    loop each interval, phase-jittered per carrier
        C->>X: AVS — flights × classes, chunked to the Type B envelope
        X->>G: relay to each distribution address
        G->>G: cache Put (an older assertion never moves state backwards)
    end
    Note over G: Decide(): open+fresh → free sale · count exhausted → ask<br/>closed → ask (no free sale, but the carrier still answers)
```

## Schedule change

```mermaid
sequenceDiagram
    participant C as Carrier
    participant G as GDS gateway
    participant Q as Queues

    C->>G: ASM CNL BA0117/16DEC (via switch)
    G->>G: applySchedule: find every booking on the flight
    G->>Q: place schedule-change item per booking
    Note over Q: each item is a booking a person now has to rework;<br/>the map's halos are these placements, counted
```

## Movement

```mermaid
sequenceDiagram
    participant C as Carrier ops
    participant X as Switch
    participant W as Watcher (ops display)

    C->>X: MVT — AD actual departure, EA estimate, delays coded
    X->>X: publish movement on own bus (transit is still a movement)
    X->>W: relay to the addressee
    Note over X: an operations display watches the whole sky<br/>from the switch — every movement crosses it
    C->>X: MVA on arrival · DIV names the alternate when diverted
```

## Tickets and EMDs

```mermaid
sequenceDiagram
    participant G as GDS gateway
    participant C as Carrier (EDIFACT)

    G->>G: IssueTickets: documents + coupons on the record
    G->>C: TKCREQ — ticket control, per document
    C->>G: TKCRES
    Note over G,C: ticket control is EDIFACT-only; a teletype carrier<br/>not told is a queued divergence, not a silent gap
```

## The ground story (wholesky's use of the name-list and bag packages)

```mermaid
sequenceDiagram
    participant C as Carrier
    participant A as Airport / sortation

    Note over C: T−180 min
    C->>A: PNL — every booked party, from the carrier's own store
    Note over C: T−90
    C->>A: BSM per party with bags (licence-plate tags)
    Note over C: T−60
    C->>A: ADL — only the diff since the PNL (silence if none)
    Note over C: departure
    C->>A: BPM — what was loaded
```
