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

Parsing is lenient by design. Diagnostics record every deviation; the only hard
failure is input with no content at all.

**Encode is not the inverse of Parse.** Parse normalises whitespace, so
round-tripping a non-conforming message produces a conforming one. Use
`Message.Raw` for audit and replay, never a re-encode.

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

`MATIPProfile()` is a starting point for MATIP-style links. It implements the
length framing only, not the RFC 2351 session layer, and several carriers run
non-conforming variants. Confirm the header layout against the carrier's
interface control document before going live.
