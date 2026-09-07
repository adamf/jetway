# Wire formats

This document states what is implemented, how far, and where the shipped
behaviour is a profile rather than a specification.

## Type B (`pkg/typeb`)

Type B is the teletype envelope that the SITA and ARINC store-and-forward
networks carry.

```
ZCZC ABC1234                 optional network start-of-message and channel
QU LHRRMBA NYCRMAA           priority code, then 7-character TTY addresses
.LONXX1A 121430              origin address and DDHHMM time group
                             optional blank line
SS                           message text
BA0175Y15JUNLHRJFKNN1
NNNN                         optional network end-of-message
```

The package implements:

- Priority codes, with the common set named and unknown codes flagged.
- Addresses as `LLLDDCC`, which is location, department and company. The
  parser accepts alphanumeric designators such as `1A`. It accepts
  non-conventional addresses with a diagnostic rather than a rejection.
- Address blocks wrapped across several lines. A line is part of the address
  block when every token on the line parses as an address.
- Origin line with day-and-time group and trailing tokens such as relay
  signatures or sequence numbers.
- `ZCZC`/`NNNN` framing and stray transport control characters.
- The `CharsetITA2` and `CharsetIA5` character repertoires. `CharsetITA2` is
  conservative and `CharsetIA5` is wider. The package validates text against
  them on egress, and they are available for sanitising relayed text.
- Line-length limits on encode. The encoder wraps address lines and never
  splits an address.
- The size limits that IATA's Type B whitepaper states: 60 lines of 63
  characters *and* under 4 KB for the whole message. The line limits do not
  imply the byte limit. A long message with a distribution list passes the
  line checks and still exceeds 4096 bytes. The encoder therefore checks the
  byte limit separately, against the bytes that go on the wire.
- `PDM`, the possible-duplicate indicator. The parser reads it from either the
  origin line or a header line of its own, and the encoder writes it to the
  origin line. The whitepaper describes it as a message header indicator and
  does not state its position. Both positions are therefore accepted.
  `MarkPossibleDuplicate` stamps it onto bytes that are already encoded.
  `pkg/egress` uses this to mark a retransmission without regenerating a
  message that the partner has already received.

Parsing is lenient by design. Diagnostics record every deviation. The only
hard failure is input with no content.

**Encode is not the inverse of Parse.** Parse normalises whitespace. A round
trip of a non-conforming message therefore produces a conforming message. Use
`Message.Raw` for audit and replay. Do not use a re-encode.

Encode guarantees that it never emits a frame that reads back as a different
message. `FuzzRoundTrip` tests this guarantee. The fuzzer found these 3
defects, all with the same cause. In each, the decoder depended on its own
output:

- An address long enough to overflow a line on its own made the encoder write
  a blank line into the address block. A blank line ends the header. The next
  address and the origin line therefore came back as message text.
- The next reader read text whose last line was `NNNN` as end-of-message
  framing. Unframed output now refuses such text rather than losing a line.
- Trailing whitespace-only lines survived Parse, but the reader on the other
  side of an encode stripped them. Parse now drops them. The parsed form is
  therefore the canonical form.

## CONTRL (`pkg/edifact`)

CONTRL is the UN/EDIFACT syntax and service report. It is the receipt that a
partner is owed for an interchange. It is also the only standard way to tell a
partner that their syntax was wrong.

CONTRL is the one message here whose conformance can be checked. The UN
publishes the UNSM definition. The segment table (UCI, UCF, UCM, UCS, UCD)
comes from that definition. So do the action codes in data element 0083 and
the whole syntax error list in 0085. None of them is inferred.

The package implements:

- `Check`, which turns the decoder's own diagnostics into a report. The
  mapping onto 0085 is partial by design. Only faithful mappings are listed.
  Anything else becomes 18, *unspecified error*, which the standard provides
  for this case. Claiming a code that we cannot justify would be worse than
  reporting that we do not know which code applies.
- `Receipt` for action 8. Action 8 states that the interchange arrived and
  states nothing about its syntax.
- Building and parsing at all 5 reporting levels, including the component
  position in S011. The component position distinguishes "this element is
  wrong" from "the second half of this composite is wrong".
- Consuming a partner's CONTRL and matching it to the interchange that it
  acknowledges. For this reason, outbound interchanges are indexed by their
  control reference.

By default, the gateway honours UNB 0031, the sender's own acknowledgement
request. A per-link policy overrides the default. The policy values are
`always`, `errors` and `never`.

## Ticket control (`pkg/padis`)

`TKCREQ` and `TKCRES` are the interline half of electronic ticketing. One
carrier issues a ticket, and the passenger flies on another carrier's
aircraft. The operating carrier must be able to tell the issuing carrier what
became of the coupon.

**The segments are a profile, and the codes are a specification.** The PADIS
message directories are paid and were not bought. The layout here is
therefore inferred. `TKT` carries the document number. `CPN` carries the
coupon number and status. The `MSG`, `ORG`, `RCI` and `TIF` segments are the
ones that the reservation messages already use.

The coupon status vocabulary is *not* inferred. IATA publishes it in the free
Airline Guide to EMD Implementation. `pkg/pnr` takes the 16 indicators in 3
classes, open, interim and final, from that guide. An earlier version of this
repository guessed that list and had it wrong in 3 places. It invented `N`,
mislabelled `X`, and omitted `Y` and `G`.

The gateway uses ticket control as follows:

- On issuance, the gateway tells each operating carrier that a document now
  covers its segment. A teletype link has no equivalent message. The gateway
  therefore cannot tell a carrier on such a link. The gateway places a
  divergence item for that carrier, because a ticket that the operating
  carrier does not know about exists only on this node.
- An inbound request applies a coupon status change and answers. The gateway
  refuses 2 changes. A coupon that is already at a final status cannot move,
  because no follow-up is permitted on a final coupon. Also, **a carrier may
  only touch a coupon that covers a segment it operates**. If any partner
  could move any coupon, the document would have no value.
- A refusal travels in `ERC`. The gateway uppercases the reason to fit UNOA,
  which has no lowercase. If the gateway dropped the reason to make the
  message encode, the partner would not learn why it was refused.

## EMD (`pkg/pnr`, `pkg/gateway`)

An electronic miscellaneous document (EMD) is the same artefact as a ticket in
every respect that this node handles. Those respects are number format, coupon
structure, status vocabulary and conjunction rules. An EMD differs from a
ticket in what a coupon buys. A coupon can buy excess baggage, a meal, a
residual balance or an airport service. The EMD replaced the paper MCO.

Every assertion here comes from IATA's free Airline Guide to EMD
Implementation, or from Resolution 722f as that guide summarises it. Where the
guide points at a paid document, this package carries the structure and not
the contents. The sub-code list is one such document, and ATPCO holds it.

There are 2 types, and the difference between them is structural:

- **EMD-A** is associated. Its value coupons are attached to flight coupons
  and lifted with them. Every coupon must name a segment that is ticketed.
- **EMD-S** is standalone and names no segment.

The package refuses a document whose type and coupons disagree. A document
that claims to be standalone and carries an association makes 2 contradictory
statements about itself.

The package enforces these rules, and each rule has a source. Each document
has one reason-for-issuance code from the 7 published groups. Each coupon has
a sub-code, because a coupon without one records a fee without recording its
purpose. Neither print status is allowed, because an EMD is never printed. A
document has at most 4 coupons, and a conjunction set has at most 4
documents.

Association has one main effect. **When a flight coupon reaches a final
status, the value coupons attached to it are lifted with it.** A passenger who
flies has used the meal that they paid for. A document that stays open behind
a flown flight is revenue that nobody accounts for. Disassociation breaks that
link for one coupon. For example, a passenger checks in without the excess
baggage that they paid for, and that one coupon needs detaching while the
document stands.

Coupon status travels over the same ticket control messages as a flight
ticket, because the guide states that the indicators are the same. The guide
names a *System Update* request for the association itself and does not give
its EDIFACT form. This package therefore cannot check whether a carrier
expects a distinct message for the association. Association is recorded
locally, and the carrier is advised over ticket control.

**Not implemented:** originating a refund or an exchange. A carrier can report
either, and this node records the report. Producing a refund or an exchange is
a fares operation. It is out of scope by the same rule that keeps NDC shopping
out.

## Divide advisories (`pkg/padis`)

A divide advisory tells a partner that a booking has become 2 bookings.

The interline divide *procedure* is in AIRIMP, which is paid, but the way
PADIS **represents** a split is public. That was enough to build the EDIFACT
half. IATA's free PNRGOV implementation guide documents the group that carries
a split (§5.6). The group holds an `EQN` that gives the number of passengers
split from or to a record. The group also holds the `RCI` segments that name
the records involved. The guide specifies the `RCI` composite too.

The segment vocabulary therefore has a source, and the message shape is a
profile. That is the same standing as the reservation messages beside it. One
field is left empty on purpose. The reservation control type in `RCI` takes a
code from the PADIS codeset directory, which is paid. Guessing which value
means "the other half of a division" would be worse than omitting a
conditional element.

The package recognises a division by an `EQN` beside more than one `RCI`. The
guide places `EQN` in the split group and nowhere else in a reservation
message. Without that test, every ordinary request would look like a division.

**Teletype partners are still not advised.** The free AIRIMP table of contents
names the message at §3.7.10, *Divided PNR Message (DVD)*, with a procedure
chapter at §3.4. Its element layout is behind the paywall. The 2 available
substitutes are both wrong. Selling the child again double-books, and
cancelling and then reselling risks losing the seats. Those carriers keep
holding one record, and every division places an item on the divergence queue.

## SSM and ASM (`pkg/ssim`)

SSM and ASM are schedule messages over Type B. The Standard Schedules Message
(SSM) describes a repeating schedule. The Ad hoc Schedule Message (ASM)
describes single flights.

**This package is a profile.** IATA's Standard Schedules Information Manual
defines SSM and ASM, and the manual is paid and was not bought. The vocabulary
and the shape are public. The public parts are the action identifiers that
each message type uses, and the order of message identifier, time mode,
action, flight, period and legs.

The field layout within those lines is inferred. The package is therefore an
extensible recogniser set in the same sense as `pkg/airimp`. Unrecognised
lines are kept verbatim.

The 2 action sets differ. `RIN` and `RRT` are ad hoc concepts. `SKD` and
`REV` are period concepts. The decoder flags an action from the wrong set and
still decodes it.

The gateway matches a schedule message against held records by flight *and*
date. It raises a task for each affected segment. A cancellation for one day
that matched on the designator alone would affect everyone booked on that
flight number all season.

## NDC orders (`pkg/ndc`)

`pkg/ndc` implements the IATA New Distribution Capability (NDC) order messages
over HTTP. The messages are `OrderCreateRQ`, `OrderRetrieveRQ`,
`OrderCancelRQ`, and the `OrderViewRS` that answers them.

The package implements orders and does not implement shopping. An NDC order
maps onto the record that this gateway already keeps. An offer is a priced
item, pricing needs fares, and fares are out of scope. The boundary works
because an `OrderCreateRQ` can carry its flights inline in
`DetailedFlightItem`. It does not have to carry them only as a reference to an
offer. The gateway refuses an `OrderCreateRQ` that names only an offer, and
the refusal states the reason.

The package implements the EDIST generation, namespace
`http://www.iata.org/IATA/EDIST`. The 17.2 and 18.1 schemas use this
namespace, and most carrier endpoints still expose it. The schemas are
published. Unlike the teletype side, this layer can therefore be checked. The
package unwraps SOAP envelopes. Plain XML works too.

The gateway turns orders into ordinary bookings and runs them through the same
pipeline as every other booking. That pipeline includes availability, free
sale, carrier messaging and queues. There is no parallel path, because a
parallel path would need to be kept in step.

**The gateway refuses payment card details before capture.** The gateway's
first rule is that raw bytes are made durable before anything interprets them.
A primary account number must never be written to a message log that has no
encryption at rest. Both rules cannot hold at once. The gateway therefore
rejects a payload that carries a card number before capture, with a `422`.

## AIRIMP (`pkg/airimp`)

AIRIMP is the reservation message grammar carried inside Type B text.

The recognised elements are flight segments, passenger names, `SSR`, `OSI`,
record locators, ticketing, contact, received-from and remarks. Anything else
becomes an `Unknown` element. The decoder preserves it verbatim with its line
number.

The segment element is the most important element:

```
BA0175Y15JUNLHRJFKNN1
│ │   │ │    │  │  │ └─ seats
│ │   │ │    │  │  └─── action or status code
│ │   │ │    │  └────── off point
│ │   │ │    └───────── board point
│ │   │ └────────────── departure date, DDMMM — no year on the wire
│ │   └──────────────── booking class
│ └──────────────────── flight number, with optional operational suffix
└────────────────────── carrier designator
```

Both the solid form and the space-separated form parse.

`Profile` is an ordered list of recognisers. To handle a carrier's private
elements without forking, clone the default profile and prepend your own
recognisers:

```go
p := airimp.Default.Clone("carrier-xx").Prepend(airimp.Recognizer{
    Name: "xx-proprietary",
    Match: func(line string) (airimp.Element, bool) { ... },
})
```

`Message.Intent()` classifies a message from its segment action codes. It does
not use a message identifier, because much interline traffic omits the
identifier.

## UN/EDIFACT (`pkg/edifact`)

`pkg/edifact` implements ISO 9735 syntax. This layer is exact. It is the most
heavily tested part of the codebase.

The package implements:

- `UNA` service string advice, including non-default separators. The decoder
  validates that each service character is plausible. A plausible character is
  printable ASCII, not alphanumeric and not whitespace. A corrupted header
  therefore fails cleanly instead of splitting the whole interchange wrongly.
- Release characters, including `??` for a literal, with a diagnostic when a
  release precedes a non-service byte.
- Repetition separators, active from syntax version 4. The decoder re-reads an
  interchange that declares version 4 with no `UNA` with version 4 rules. It
  does this only when `UNB` `S001` is coherent and the re-read reproduces the
  version that caused it. Otherwise, decoding would not be a fixed point.
- Empty elements and components preserved by position. The encoder truncates
  trailing empties, as the standard requires.
- Envelope validation. The decoder matches `UNB`/`UNZ` control references and
  counts, `UNG`/`UNE` functional groups, and `UNH`/`UNT` references and
  segment counts. These checks catch a truncated or spliced interchange, which
  is what a store-and-forward link produces.
- Character repertoires `UNOA` and `UNOB` exactly. `UNOC` through `UNOK` are
  approximated as printable 8-bit. `UNOY` is UTF-8.
- `Interchange.Finalize()`, which recomputes `UNT` and `UNZ` counts from the
  current contents. Miscounted trailers are the most common EDIFACT
  integration defect, and avoiding them is mechanical.

`FuzzRoundTrip` asserts that anything decodable re-encodes and re-decodes to
the same bytes. It has found 6 defects so far. The first 2 were a
whitespace-only segment tag and a released line break. The next 2 were a
6-character tag that collided with `UNA` and an out-of-range syntax version
that drove a rescan. The last 2 were a `UNA`-implied repetition setting
dropped on re-encode and an explicit `UNA` dropped when the syntax matched the
defaults.

Every defect had the same cause. Decoding depended on its own output. None of
them would have been found by hand. Run the fuzzer in CI.

## PADIS (`pkg/padis`)

PADIS is the IATA message mapping over EDIFACT.

The shipped profile has these segments:

| Segment | Carries |
| --- | --- |
| `MSG` | Message function |
| `ORG` | Originator: system, office, agent |
| `TIF` | Traveller names |
| `TVL` | Travel product: dates, board and off points, carrier, flight, class |
| `RPI` | Seat count and status for the preceding `TVL` |
| `SSR` | Special service requests |
| `RCI` | Reservation control information: the locators each party holds |
| `IFT` | Free text: contacts, remarks, other service information |

The package builds and applies `PAOREQ` and `PAORES`. It builds `PNRGOV` and
`PAXLST` for the pushes to a state and applies them when they arrive.
`TKCREQ` and `TKCRES` carry ticket control (`pkg/gateway/ticket.go`).
`DCQCKI` and `DCRCKI` carry through check-in (`pkg/iatci`,
`pkg/gateway/through.go`). Each of these maps onto a record.

`Profile.Handlers` is a map from segment tag to handler. Override a handler
for a link whose composition differs. A segment that no handler claims becomes
an unparsed fragment on the record. It does not become an error.

**The segment shapes are verified against the published guide.** IATA
publishes the PNRGOV EDIFACT Implementation Guide openly, and it documents the
same PADIS segment composition that these messages use. Checking the segment
shapes above against it corrected 4 things. The traveller type in `TIF` was
read as a title. `TVL` element 3 is a marketing/operating carrier pair rather
than one code. `ARNK` and `OPEN` segments, where the date and city pair are
conditional, were discarded as unparsed fragments.

Carrier-specific deviations still exist, and a partner's own implementation
guide remains authoritative for their link.

## Availability (`pkg/avail`, `pkg/avs`)

`pkg/avail` is the store, and it is independent of any message format. AVS
pushes status over teletype, NDC returns it inside an offer, and direct access
answers per shop. All 3 sources land in the same cache. A consumer of
availability does not distinguish between the sources.

The decision depends on 2 fields. **Age** matters because a status is a claim
about a moment, and a stale claim is not evidence. Past the trust window, a
lookup reports Unknown, and the booking sends a request to the carrier
instead. **Source** matters because a broadcast, a direct answer and an
operator override are not equally authoritative. A weaker source must not
overwrite a fresher, stronger source.

These fields drive this decision:

| Belief | Action | Wire |
| --- | --- | --- |
| Open | sell now, tell the carrier after | `SS` |
| Open, fewer seats than wanted | ask | `NN` |
| Closed | refuse before sending anything | — |
| Waitlist | ask for the waitlist | `LL` |
| Unknown or stale | ask | `NN` |

Unknown is not Closed, by design. Closed is the carrier's answer. Unknown is
our lack of information. Conflating them either blocks sellable inventory or
sells inventory that nobody offered.

`pkg/avs` decodes the messages. The normative source is AIRIMP Chapter 4,
which is paid. Its published contents page names the shape. The shape is
status code families C, AS, L and LA, then numeric availability as Options 1,
2 and 3, **each marked bilateral**. The 3 numbered options are explicitly
bilateral. This is the standard stating that the numeric form is agreed per
partner.

The grammar is therefore a profile, and the code meanings are configuration.
The standard describes this arrangement. It is not a way around the paywall.

The default status map holds only `O`, `C`, `L` and `R`. The meaning of these
codes is not in doubt. `AS`, `LA` and the numeric families are absent by
design. An unmapped code produces an error diagnostic that names it. A guess
would grant free sale on a class that the carrier may have closed. Configure
these codes per link from the partner's agreement.

## Status codes (`pkg/rescode`)

`pkg/rescode` holds the vocabulary that both formats share.

| Category | Codes |
| --- | --- |
| Request | `NN` `LL` `SS` `DS` `GN` `PE` `RQ` |
| Reply | `KK` `KL` `UC` `UN` `US` `UU` `NO` `TK` `TL` |
| Holding | `HK` `HL` `HN` `RR` `PN` |
| Cancel | `XX` `XK` `HX` |
| Advice | `SC` `WK` `WL` `WN` `IX` `DL` `MM` |

`ReplyTo` maps a reply to the holding that a requester should record:
`KK`→`HK`, `US`→`HL`, `UC`→ nothing held. Private bilateral codes parse and
report an unknown category. They do not fail.

## Transport (`pkg/transport`)

`LengthPrefix` covers most carrier links. Header width, byte order, and
whether the count includes the header are configuration. They are not code.
`Sentinel` frames on a terminating byte sequence. Type B arrives this way on
links that carry the classic end-of-message.

## MATIP (`pkg/matip`)

`pkg/matip` implements RFC 2351 from the RFC itself. The RFC is an open IETF
document. This layer can therefore be exact.

The header is 4 bytes. It holds 5 zero bits, a 3-bit version that must be
001, a control flag, and a 7-bit command. The last field is a 16-bit length
**that covers the whole packet, including the header**. The length rule
bounds a Type B message carried in one packet to 65,531 bytes.

Type B uses 3 control commands and a data packet. The control commands are
session open, open confirm and session close. The payload of the data packet
is the Type B message. The handshake settles character coding, the
responsibility-transfer protocol, and optionally the host identifiers. Either
side can initiate. A collision is broken in favour of the higher IP address.

The package implements the full Type B packet and session layer, on IANA port
351. It does not implement Type A, which is interactive terminal traffic on
port 350. It does not implement BATAP acknowledgement semantics above MATIP
either.

The RFC has one inconsistency. It places the host identifiers at "bytes 9,10
and 11,12". That position cannot be reconciled with the 10-byte packet length
that the RFC states 2 paragraphs earlier. The bit diagram and the stated
lengths agree with each other. This implementation therefore follows them and
puts the identifiers at offsets 6..9. Confirm the position against a partner's
interface control document.
