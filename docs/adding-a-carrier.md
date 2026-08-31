# Adding a carrier link

Onboarding a partner is four questions. The first two are configuration; the
rest only come up when their dialect differs from the shipped profile.

## 1. How do the bytes arrive, and how is the sender identified?

These are one question, because a listener answers both.

**Over HTTPS.** The easiest partner to onboard, and the only one that needs no
network contract and no agreed framing:

```yaml
ingress:
  - name: partners-https
    type: https
    addr: 0.0.0.0:8443
    tls:
      cert: /etc/jetway/tls/server.crt
      key: /etc/jetway/tls/server.key
      client_ca: /etc/jetway/tls/partners-ca.crt
    identify:
      by_cert_cn:
        gateway.xx.example.com: XX
    synchronous: true    # hold the request open and return the reply inline
```

**Over a circuit.** Most carrier interface control documents describe a
length-prefixed stream differing only in header width, byte order, and whether
the count includes the header:

```yaml
  - name: link-xx
    type: tcp
    addr: 0.0.0.0:9110
    framing:
      kind: length_prefix
      header_bytes: 2
      inclusive: true
      max_bytes: 1048576
    tls: {cert: ..., key: ..., client_ca: ...}
    identify:
      by_cert_cn: {res.xx.example.com: XX}
```

For links carrying the classic teletype end-of-message, use
`framing: {kind: sentinel, terminator: "\nNNNN\n"}` instead.

Get the framing wrong and every symptom looks like a parser bug. Verify it
against a capture before anything else: a correctly framed message decodes or
produces coherent diagnostics, whereas a misframed one produces nonsense at a
random offset.

**By file drop.** Run a real SFTP server and point Jetway at the directory it
writes into:

```yaml
  - name: xx-batch
    type: filedrop
    dir: /var/spool/jetway/in/xx
    pattern: "*.msg"
    stable_for: 5s       # do not read a file the partner is still uploading
    identify: {peer: XX}
```

Note what `identify` never offers: a way to take the peer name from the message.
A certificate signed by your CA but not listed under `by_cert_cn` is refused,
not treated as a default. `by_cidr` is weaker and only defensible on a private
circuit; `identify.peer` assumes nothing else can reach the port.

## 2. How do we reach them?

```yaml
peers:
  - name: XX
    carrier: XX          # routes outbound: a booking on XX0175 goes to this link
    format: typeb        # or edifact
    tty_address: XXXRMXX
    egress:
      type: https_post   # or tcp_accept, tcp_dial, filedrop
      url: https://gateway.xx.example.com/jetway/messages
      tls: {cert: ..., key: ..., client_ca: ...}
      retry: {max_attempts: 12, initial: 2s, max: 10m}
```

`tcp_accept` means they connect to us and replies go back down that session,
which is the usual arrangement when we host the listener.

Check it before starting anything:

```sh
jetwayd -config /etc/jetway/jetway.yaml -print-config
```

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

Post a captured message as the partner would, using their certificate:

```sh
curl --cacert ca.crt --cert xx.crt --key xx.key \
     --data-binary @captured.tty \
     -H 'Content-Type: application/octet-stream' \
     https://gateway.example.com:8443/messages
```

A 202 means the bytes are durable. A 403 means the certificate did not map to a
peer. A 503 means the pipeline would not take it and they should retransmit.

Or point a `carriersim` at your gateway and drive it from the other side:

```sh
go run ./cmd/carriersim -carrier XX -format typeb -tty XXXRMXX \
  -link 127.0.0.1:9110 -http 127.0.0.1:9500

curl -s localhost:9500/pnrs        # what the carrier believes
curl -s localhost:9500/inventory   # what it has sold
```

Comparing both sides is the test that matters. A booking is only correct if the
gateway and the carrier agree about it.

For a regression test, the pattern in `pkg/gateway/e2e_test.go` wires two
gateways together with direct calls — no sockets, fully deterministic — and
asserts on both records, both message logs, and the event trail.

## Checklist before going live

- [ ] `jetwayd -print-config` shows the listener with `mtls=true`.
- [ ] The partner's certificate common name is mapped under `by_cert_cn`, and an
      unmapped certificate is confirmed to get a 403.
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
