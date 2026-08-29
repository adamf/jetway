# Wire formats

What is implemented, how far, and where the shipped behaviour is a profile
rather than a specification.

## Type B (`pkg/typeb`)

The teletype envelope carried by the SITA and ARINC store-and-forward networks.

```
ZCZC ABC1234                 optional network start-of-message and channel
QU LHRRMBA NYCRMAA           priority code, then 7-character TTY addresses
.LONXX1A 121430              origin address and DDHHMM time group
                             optional blank line
SS                           message text
BA0175Y15JUNLHRJFKNN1
NNNN                         optional network end-of-message
```

Implemented:

- Priority codes, with the common set named and unknown ones flagged.
- Addresses as `LLLDDCC` — location, department, company — accepting
  alphanumeric designators such as `1A`, and non-conventional addresses with a
  diagnostic rather than a rejection.
- Address blocks wrapped across several lines, distinguished from message text
  by whether every token on the line parses as an address.
- Origin line with day-and-time group and trailing tokens such as relay
  signatures or sequence numbers.
- `ZCZC`/`NNNN` framing and stray transport control characters.
- Character repertoires: `CharsetITA2` (conservative) and `CharsetIA5` (wider),
  validated on egress and available for sanitising relayed text.
- Line-length limits on encode, wrapping address lines without ever splitting
  an address.
- The size limits IATA's Type B whitepaper states: 60 lines of 63 characters
  *and* under 4 KB for the whole message. The byte limit is not implied by the
  line limits — a long message with a distribution list clears the line checks
  and still exceeds 4096 bytes — so it is checked separately, against what
  actually goes on the wire.
- `PDM`, the possible-duplicate indicator, read from either the origin line or
  a header line of its own and written to the origin line. The whitepaper
  describes it as a message header indicator without stating its position,
  so both readings are accepted. `MarkPossibleDuplicate` stamps it onto already
  encoded bytes, which is how `internal/egress` marks a retransmission without
  regenerating a message the partner has already been told about.

Parsing is lenient by design. Diagnostics record every deviation; the only hard
failure is input with no content at all.

**Encode is not the inverse of Parse.** Parse normalises whitespace, so
round-tripping a non-conforming message produces a conforming one. Use
`Message.Raw` for audit and replay, never a re-encode.

What Encode does guarantee is that it never emits a frame that reads back as a
different message, and `FuzzRoundTrip` holds it to that. Three defects came out
of it, all the same shape — the decoder depending on its own output:

- An address long enough to overflow a line on its own left the encoder writing
  a blank line into the address block. A blank line ends the header, so the next
  address and the origin line came back as message text.
- Text whose last line was literally `NNNN` was swallowed as end-of-message
  framing by the next reader. Unframed output now refuses it rather than losing
  a line.
- Trailing whitespace-only lines survived Parse but were stripped by the reader
  on the other side of an encode. Parse now drops them, so the parsed form is
  the canonical one.

## AIRIMP (`pkg/airimp`)

The reservation message grammar carried inside Type B text.

Recognised elements: flight segments, passenger names, `SSR`, `OSI`, record
locators, ticketing, contact, received-from and remarks. Anything else becomes
an `Unknown` element, preserved verbatim with its line number.

The segment element is the load-bearing one:

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

Both the solid and space-separated forms parse.

`Profile` is an ordered list of recognizers. Clone the default and prepend your
own to handle a carrier's private elements without forking:

```go
p := airimp.Default.Clone("carrier-xx").Prepend(airimp.Recognizer{
    Name: "xx-proprietary",
    Match: func(line string) (airimp.Element, bool) { ... },
})
```

`Message.Intent()` classifies a message from its segment action codes rather
than from a message identifier, because much interline traffic omits the
identifier entirely.

## UN/EDIFACT (`pkg/edifact`)

ISO 9735 syntax. This layer is exact, and is the most heavily tested part of the
codebase.

Implemented:

- `UNA` service string advice, including non-default separators, with service
  characters validated as plausible — not alphanumeric, not whitespace, printable
  ASCII — so a corrupted header fails cleanly instead of shredding the interchange.
- Release characters, including `??` for a literal, with a diagnostic when a
  release precedes a non-service byte.
- Repetition separators, active from syntax version 4. An interchange that
  declares version 4 with no `UNA` is re-read with version 4 rules, but only
  when `UNB` `S001` is itself coherent and the re-read reproduces the version
  that motivated it — otherwise decoding would stop being a fixed point.
- Empty elements and components preserved positionally; trailing empties
  truncated on encode as the standard requires.
- Envelope validation: `UNB`/`UNZ` control reference matching and counts,
  `UNG`/`UNE` functional groups, `UNH`/`UNT` reference matching and segment
  counts — the checks that catch a truncated or spliced interchange, which is
  exactly what a store-and-forward link produces.
- Character repertoires `UNOA` and `UNOB` exactly; `UNOC` through `UNOK`
  approximated as printable 8-bit; `UNOY` as UTF-8.
- `Interchange.Finalize()` recomputes `UNT` and `UNZ` counts from the current
  contents. Miscounted trailers are the most common EDIFACT integration defect
  and are entirely mechanical to avoid.

`FuzzRoundTrip` asserts that anything decodable re-encodes and re-decodes to the
same bytes. It has found six real defects so far: a whitespace-only segment
tag, a released line break, a six-character tag colliding with `UNA`, an
out-of-range syntax version driving a rescan, a `UNA`-implied repetition setting
dropped on re-encode, and an explicit `UNA` dropped when the syntax happened to
match the defaults. Every one of them is the same shape — decoding depending on
its own output — and none would have been found by hand. Run it in CI.

## PADIS (`pkg/padis`)

IATA message mapping over EDIFACT.

The segments in the shipped profile:

| Segment | Carries |
| --- | --- |
| `MSG` | Message function |
| `ORG` | Originator: system, office, agent |
| `TIF` | Traveller names |
| `TVL` | Travel product: dates, board and off points, carrier, flight, class |
| `RPI` | Seat count and status for the preceding `TVL` |
| `SSR` | Special service requests |
| `RCI` | Reservation control information — the locators each party holds |
| `IFT` | Free text: contacts, remarks, other service information |

`PAOREQ` and `PAORES` are built and applied. `PNRGOV`, `PAXLST`, `TKCREQ`,
`TKCRES`, `DCQCKI` and `DCRCKI` are named constants that decode at the syntax
layer and route, but do not yet map onto a record.

`Profile.Handlers` is a map from segment tag to handler. Override one for a link
whose composition differs; segments no handler claims become unparsed fragments
on the record rather than errors.

**Verified against the published guide, not guessed.** IATA publishes the
PNRGOV EDIFACT Implementation Guide openly, and it documents the same PADIS
segment composition these messages use. The segment shapes above were checked
against it, which corrected four things: the traveller type in `TIF` was being
read as a title, `TVL` element 3 is a marketing/operating carrier pair rather
than one code, and `ARNK` and `OPEN` segments -- where date and city pair are
conditional -- were being discarded as unparsed fragments.

Carrier-specific deviations still exist, and a partner's own implementation
guide remains authoritative for their link.

## Availability (`pkg/avail`, `pkg/avs`)

`pkg/avail` is the store and knows no message format. AVS pushes status over
teletype, NDC returns it inside an offer, direct access answers per shop; all
three land in the same cache, and what consumes availability does not care
which.

Two fields are load-bearing. **Age**, because a status is a claim about a
moment and a stale claim is not evidence — past the trust window a lookup
reports Unknown and booking asks the carrier instead. **Source**, because a
broadcast, a direct answer and an operator override are not equally
authoritative, and a weaker source must not overwrite a fresher stronger one.

The decision this drives:

| Belief | Action | Wire |
| --- | --- | --- |
| Open | sell now, tell the carrier after | `SS` |
| Open, fewer seats than wanted | ask | `NN` |
| Closed | refuse before sending anything | — |
| Waitlist | ask for the waitlist | `LL` |
| Unknown or stale | ask | `NN` |

Unknown is deliberately not Closed. One is the carrier's answer, the other is
our ignorance; conflating them either blocks sellable inventory or sells
inventory nobody offered.

`pkg/avs` decodes the messages. The normative source is AIRIMP Chapter 4, which
is paid — but its published contents page names the shape: status code families
C, AS, L and LA, then numeric availability as Options 1, 2 and 3, **each marked
bilateral**. Three numbered options that are explicitly bilateral is the
standard saying the numeric form is agreed per partner. So the grammar is a
profile and the code meanings are configuration, which is what the standard
describes rather than a way around the paywall.

The default status map holds only `O`, `C`, `L` and `R` — codes whose meaning
is not in doubt. `AS`, `LA` and the numeric families are deliberately absent: an
unmapped code produces an error diagnostic naming it, where a guess would
quietly grant free sale on a class the carrier may have closed. Configure them
per link from the partner's agreement.

## Status codes (`pkg/rescode`)

The vocabulary both formats share.

| Category | Codes |
| --- | --- |
| Request | `NN` `LL` `SS` `DS` `GN` `PE` `RQ` |
| Reply | `KK` `KL` `UC` `UN` `US` `UU` `NO` `TK` `TL` |
| Holding | `HK` `HL` `HN` `RR` `PN` |
| Cancel | `XX` `XK` `HX` |
| Advice | `SC` `WK` `WL` `WN` `IX` `DL` `MM` |

`ReplyTo` maps a reply to the holding a requester should record: `KK`→`HK`,
`US`→`HL`, `UC`→ nothing held. Private bilateral codes parse fine and report an
unknown category rather than failing.

## Transport (`internal/transport`)

`LengthPrefix` covers most carrier links: header width, byte order, and whether
the count includes the header are configuration, not code. `Sentinel` frames on
a terminating byte sequence, which is how Type B arrives on links that carry the
classic end-of-message.

## MATIP (`pkg/matip`)

RFC 2351, implemented from the RFC rather than guessed at. It is an open IETF
document, so this layer can be exact.

The header is four bytes: five zero bits, a three-bit version that must be 001,
a control flag, a seven-bit command, and a sixteen-bit length **covering the
whole packet including the header**. That last detail bounds a Type B message
carried in one packet to 65,531 bytes.

Type B uses three control commands -- session open, open confirm, session close
-- and a data packet whose payload is the Type B message. The handshake settles
character coding, the responsibility-transfer protocol, and optionally the host
identifiers; either side may initiate, and a collision is broken in favour of
the higher IP address.

Implemented: the full Type B packet and session layer, on IANA port 351.
Not implemented: Type A (interactive terminal traffic, port 350), and BATAP
acknowledgement semantics above MATIP.

One inconsistency is worth knowing about. The RFC places the host identifiers
at "bytes 9,10 and 11,12", which cannot be reconciled with the 10-byte packet
length it states two paragraphs earlier. The bit diagram and the stated lengths
agree with each other, so this implementation follows those and puts the
identifiers at offsets 6..9. Confirm against a partner's interface control
document.
