# Adding a carrier link

Onboarding a partner is four questions. None of them require modifying this
repository.

## 1. How do the bytes arrive?

Pick or configure a framer. Most carrier interface control documents describe a
length-prefixed TCP stream that differs from the next one only in header width,
byte order, and whether the count includes the header:

```go
framer := transport.LengthPrefix{
    HeaderBytes:  2,     // or 4
    LittleEndian: false, // network links are almost always big endian
    Inclusive:    true,  // does the count include the header?
    Max:          1 << 20,
    Label:        "carrier-xx",
}
```

For links carrying the classic teletype end-of-message, frame on the sentinel
instead:

```go
framer := transport.TypeBSentinel()   // terminates on "\nNNNN\n"
```

Get this wrong and every symptom looks like a parser bug. Verify it against a
capture before anything else: a correctly framed message decodes or produces
coherent diagnostics, whereas a misframed one produces nonsense at a random
offset.

## 2. Who is on the other end?

```go
gw.AddPeer(&gateway.Peer{
    Name:       "XX",                  // link name; the store's peer key
    Carrier:    "XX",                  // designator whose segments this link owns
    Format:     store.FormatTypeB,     // or store.FormatEDIFACT
    TTYAddress: "XXXRMXX",             // their Type B address
})
```

`Carrier` is what routes an outbound request: a booking on `XX0175` goes to the
link whose `Carrier` is `XX`.

## 3. What dialect do they speak?

Start with the default profile and find out. Run traffic through, then look at
what arrives as unparsed — the console surfaces it per record, and
`jetwayctl decode` shows it for a captured file:

```sh
jetwayctl decode captured.tty
```

Lines marked `?` are what the profile did not claim.

For teletype, add a recognizer ahead of the defaults:

```go
xx := airimp.Default.Clone("carrier-xx").Prepend(airimp.Recognizer{
    Name: "xx-seat-element",
    Match: func(line string) (airimp.Element, bool) {
        if !strings.HasPrefix(line, "STX ") {
            return nil, false
        }
        return &airimp.Remark{Text: strings.TrimPrefix(line, "STX ")}, true
    },
})
peer.AirimpProfile = xx
```

For EDIFACT, override a segment handler:

```go
xx := padis.Default.Clone("carrier-xx")
xx.Handlers["TVL"] = func(p *pnr.PNR, seg edifact.Segment, st *padis.State,
    opts padis.ApplyOptions) ([]padis.Change, bool) {
    // this carrier puts the class in element 5, not element 4
}
peer.PadisProfile = xx
```

Ordering matters for recognizers: they are tried first to last, and the
keyword-led elements must come before the positional ones. `Prepend` puts yours
ahead of everything.

## 4. Can they answer, or only ask?

If the link only receives requests from you, leave `Responder` nil. If this node
answers — because it is a carrier, or because you are running a simulator —
implement the interface:

```go
type Responder interface {
    Decide(ctx context.Context, p *pnr.PNR, peer *Peer) (map[string]string, error)
}
```

Return a status code keyed by `pnr.Segment.Key()`. Answer only segments in a
requested state; re-answering one already at `HK` double-counts the seats.
`gateway.Inventory` is a worked example.

## Testing a new link

Point a `carriersim` at your gateway and drive it from the other side:

```sh
go run ./cmd/carriersim -carrier XX -format typeb -tty XXXRMXX \
  -link 127.0.0.1:9100 -http 127.0.0.1:9500

curl -s localhost:9500/pnrs        # what the carrier believes
curl -s localhost:9500/inventory   # what it has sold
```

Comparing both sides is the test that matters. A booking is only correct if the
gateway and the carrier agree about it.

For a regression test, the pattern in `internal/gateway/e2e_test.go` wires two
gateways together with direct calls — no sockets, fully deterministic — and
asserts on both records, both message logs, and the event trail.

## Checklist before going live

- [ ] Framing confirmed against the interface control document, not inferred.
- [ ] A captured message from the partner decodes with no `error` diagnostics.
- [ ] Unparsed fragments reviewed; anything meaningful has a recognizer.
- [ ] The dedup key is right for this link. If the partner sends a sequence
      number, use it — the teletype fallback keys on originator, time group and
      digest, which treats two byte-identical messages in one minute as one.
- [ ] Character repertoire agreed. `ITA2` is conservative; confirm before
      sending anything wider.
- [ ] Test traffic is marked as test. The gateway refuses interchanges with the
      `UNB` test indicator set, which is only useful if the partner sets it.
- [ ] Both sides agree on a booking end to end, checked against the carrier's
      own record and not only ours.
