// Package gateway is the message pipeline: it turns bytes from a peer into
// changes on a record, and changes on a record into bytes for a peer.
//
// The pipeline has one non-negotiable rule. Raw bytes are made durable before
// anything interprets them, and every later stage is a pure function of those
// bytes plus configuration. That is what makes replay possible after a parser
// fix, and it is why a decode failure produces a message sitting in the dead
// letter queue rather than a booking that never happened.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/internal/ulid"
	"github.com/adamf/jetway/pkg/airimp"
	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/avs"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/typeb"
)

// Identity is how this node names itself to partners.
type Identity struct {
	// Designator is the two-character company code, e.g. a GDS code or an
	// airline designator.
	Designator string
	// TTYAddress is the seven-character Type B address messages are sent from.
	TTYAddress string
	// Name is a human label for the console.
	Name string
}

// Peer is a configured partner link.
type Peer struct {
	// Name is the link name, used for routing and as the store's peer key.
	Name string
	// Carrier is the designator whose segments this peer is authoritative for.
	Carrier string
	// Format is the wire encoding this peer speaks.
	Format store.Format
	// TTYAddress is the peer's Type B address, used when Format is typeb.
	TTYAddress string
	// AirimpProfile overrides the AIRIMP grammar for this link. Nil uses the
	// default profile.
	AirimpProfile *airimp.Profile
	// PadisProfile overrides the PADIS segment handlers for this link.
	PadisProfile *padis.Profile
	// AvsProfile overrides the availability grammar and status-code meanings
	// for this link. The standard makes numeric availability bilateral, so a
	// per-link map is the normal case rather than an escape hatch.
	AvsProfile *avs.Profile
}

func (p *Peer) avs() *avs.Profile {
	if p.AvsProfile != nil {
		return p.AvsProfile
	}
	return avs.Default
}

func (p *Peer) airimp() *airimp.Profile {
	if p.AirimpProfile != nil {
		return p.AirimpProfile
	}
	return airimp.Default
}

func (p *Peer) padis() *padis.Profile {
	if p.PadisProfile != nil {
		return p.PadisProfile
	}
	return padis.Default
}

// Sender hands an outbound message to a transport.
type Sender interface {
	Send(ctx context.Context, peer string, raw []byte) error
}

// SenderFunc adapts a function to Sender.
type SenderFunc func(ctx context.Context, peer string, raw []byte) error

func (f SenderFunc) Send(ctx context.Context, peer string, raw []byte) error {
	return f(ctx, peer, raw)
}

// Gateway is a message-processing node. One instance is a GDS; another,
// configured with a carrier identity and a Responder, is an airline system.
type Gateway struct {
	Identity Identity
	Store    store.Store
	Bus      *Bus
	Log      *slog.Logger
	Sender   Sender

	// Responder decides how to answer an inbound request. Nil means this node
	// does not answer requests, which is the right behaviour for a distribution
	// system that only originates them.
	Responder Responder

	// Avail is what this node believes is sellable. Nil disables free sale,
	// and every segment is then requested -- correct, just slower.
	Avail *avail.Cache

	locators *pnr.LocatorAllocator

	mu    sync.RWMutex
	peers map[string]*Peer
	// byCarrier routes an outbound message for a carrier to the link that
	// serves it.
	byCarrier map[string]*Peer
}

// New builds a gateway.
func New(id Identity, st store.Store, bus *Bus, log *slog.Logger, locatorSecret []byte) *Gateway {
	return &Gateway{
		Identity:  id,
		Store:     st,
		Bus:       bus,
		Log:       log,
		locators:  pnr.NewLocatorAllocator(locatorSecret),
		peers:     map[string]*Peer{},
		byCarrier: map[string]*Peer{},
	}
}

// AddPeer registers a link.
func (g *Gateway) AddPeer(p *Peer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.peers[p.Name] = p
	if p.Carrier != "" {
		g.byCarrier[p.Carrier] = p
	}
}

// Peer returns a configured peer by link name.
func (g *Gateway) Peer(name string) *Peer {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.peers[name]
}

// Peers returns every configured peer.
func (g *Gateway) Peers() []*Peer {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*Peer, 0, len(g.peers))
	for _, p := range g.peers {
		out = append(out, p)
	}
	return out
}

// PeerForCarrier returns the link serving a carrier's segments.
func (g *Gateway) PeerForCarrier(carrier string) *Peer {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.byCarrier[carrier]
}

// Responder answers an inbound request. A carrier implements this to consult
// its inventory; a distribution system usually leaves it nil.
type Responder interface {
	// Decide returns a status code for each segment key on the record.
	Decide(ctx context.Context, p *pnr.PNR, peer *Peer) (map[string]string, error)
}

// IngestOptions parameterises a single inbound message.
type IngestOptions struct {
	// Transport names the ingress that accepted the message, for the audit
	// trail. Empty defaults to "link".
	Transport string
	// Remote describes where it came from: an address, a certificate subject,
	// or a file path.
	Remote string
	// FromFile marks a message read from a drop directory rather than a live
	// link, which permits leniencies that would be unsafe on relayed traffic.
	FromFile bool
	// HoldReply returns a generated reply in the Result instead of sending it
	// over the peer's egress. A partner posting over HTTP and waiting on the
	// response has no egress to receive a reply on, and attempting one would
	// queue an undeliverable message for a link that does not exist.
	HoldReply bool
}

// Result reports what processing a message did.
type Result struct {
	MessageID string
	PNRID     string
	Locator   string
	Status    store.Status
	Duplicate bool
	Changes   []string
	Replies   []string // ids of messages sent in response
	// Reply carries the generated response when HoldReply was set.
	Reply []byte
	Err   error
}

// NewMessageID mints an identifier for a message that will enter the pipeline
// later, so a spooled message keeps one identity from the moment it lands on
// disk through to the store.
func NewMessageID() string { return ulid.New() }

// trace publishes a pipeline step.
func (g *Gateway) trace(msgID, step, detail string) {
	g.Bus.Publish(EvTrace, map[string]any{
		"node": g.Identity.Name, "message_id": msgID, "step": step, "detail": detail,
	})
}

// Ingest is the inbound pipeline.
//
// The ordering here is the contract: capture, then classify, then decode, then
// deduplicate, then apply, then respond. Capture happens first and
// unconditionally so that nothing after it can lose the message.
func (g *Gateway) Ingest(ctx context.Context, peerName string, raw []byte) (*Result, error) {
	return g.IngestWith(ctx, peerName, raw, IngestOptions{})
}

// IngestWith is Ingest with per-message options.
func (g *Gateway) IngestWith(ctx context.Context, peerName string, raw []byte, opts IngestOptions) (*Result, error) {
	now := time.Now().UTC()
	sum := sha256.Sum256(raw)

	peer := g.Peer(peerName)
	if peer == nil {
		// An unknown peer is a configuration problem, not a reason to drop
		// traffic. Capture it against a synthetic link and let an operator see
		// it in the dead letter queue.
		peer = &Peer{Name: peerName, Format: store.FormatUnknown}
	}

	transport := opts.Transport
	if transport == "" {
		transport = "link"
	}
	msg := &store.Message{
		ID: ulid.NewAt(now), Direction: store.Inbound, At: now,
		Transport: transport, Peer: peerName, Format: store.FormatUnknown,
		Raw: raw, SHA256: hex.EncodeToString(sum[:]), Size: len(raw),
		Status: store.StatusReceived,
	}
	if err := g.Store.AppendMessage(ctx, msg); err != nil {
		// Durability failed, so the message is not safe. Return the error and
		// let the transport decline to acknowledge; the peer will retransmit.
		return nil, fmt.Errorf("gateway: capture inbound: %w", err)
	}
	g.Bus.Publish(EvMessage, g.msgView(msg))
	g.trace(msg.ID, "captured", fmt.Sprintf("%d bytes from %s", len(raw), peerName))

	res := &Result{MessageID: msg.ID}
	if err := g.process(ctx, peer, msg, res, opts); err != nil {
		msg.Status = store.StatusDLQ
		msg.Error = err.Error()
		res.Status = store.StatusDLQ
		res.Err = err
		g.Log.Error("message routed to dead letter queue",
			"id", msg.ID, "peer", peerName, "err", err)
		g.trace(msg.ID, "dlq", err.Error())
	}
	if uerr := g.Store.UpdateMessage(ctx, msg); uerr != nil {
		g.Log.Error("failed to record message outcome", "id", msg.ID, "err", uerr)
	}
	g.Bus.Publish(EvMessage, g.msgView(msg))
	return res, nil
}

// process decodes and applies a captured message.
func (g *Gateway) process(ctx context.Context, peer *Peer, msg *store.Message, res *Result, opts IngestOptions) error {
	dec, err := g.decode(peer, msg, opts)
	if err != nil {
		return err
	}
	msg.Format = dec.Format
	msg.Kind = dec.Kind
	msg.DedupKey = dec.DedupKey
	msg.Diagnostics = dec.Diagnostics
	msg.Status = store.StatusDecoded
	g.trace(msg.ID, "decoded", string(dec.Format)+" "+dec.Kind)

	// A retransmission is normal on a store-and-forward link. Record it, then
	// decline to apply it: applying a sell twice books two seats.
	if dec.DedupKey != "" {
		if prev, seen, err := g.Store.FindByDedupKey(ctx, peer.Name, dec.DedupKey); err == nil && seen && prev != msg.ID {
			msg.Status = store.StatusRejected
			msg.Error = "duplicate of " + prev
			msg.CorrelationID = prev
			res.Duplicate = true
			res.Status = store.StatusRejected
			g.trace(msg.ID, "duplicate", "already processed as "+prev)
			return nil
		}
	}

	if dec.Test {
		msg.Status = store.StatusRejected
		msg.Error = "test interchange refused on a production link"
		res.Status = store.StatusRejected
		g.trace(msg.ID, "rejected", "test indicator set")
		return nil
	}

	if dec.AVS != nil {
		return g.applyAvailability(ctx, msg, dec, res)
	}
	return g.apply(ctx, peer, msg, dec, res, opts)
}

// applyAvailability folds an availability message into the cache.
//
// Availability messages touch no booking, so they never reach the record path.
// Treating them as a PNR update would create a record per broadcast.
func (g *Gateway) applyAvailability(ctx context.Context, msg *store.Message, dec *decoded, res *Result) error {
	if g.Avail == nil {
		msg.Status = store.StatusRejected
		msg.Error = "this node holds no availability cache"
		res.Status = store.StatusRejected
		return nil
	}
	applied, superseded := 0, 0
	for _, e := range dec.AVS.Entries {
		if g.Avail.Put(e) {
			applied++
			res.Changes = append(res.Changes, "availability: "+e.String())
		} else {
			superseded++
		}
	}
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	g.trace(msg.ID, "availability", fmt.Sprintf("%d applied, %d superseded", applied, superseded))
	g.Bus.Publish(EvAvail, map[string]any{
		"node": g.Identity.Designator, "applied": applied, "superseded": superseded,
		"held": g.Avail.Len(),
	})
	return nil
}

// apply folds a decoded message into a record, retrying on a version conflict.
func (g *Gateway) apply(ctx context.Context, peer *Peer, msg *store.Message, dec *decoded, res *Result, opts IngestOptions) error {
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rec, existing, err := g.resolveRecord(ctx, dec)
		if err != nil {
			return err
		}
		expected := rec.Version
		changes := dec.applyTo(rec, peer, msg.At)
		rec.UpdatedAt = msg.At

		events := make([]store.Event, 0, len(changes))
		for _, c := range changes {
			events = append(events, store.Event{
				Type: c.Op, Detail: c.Detail, MessageID: msg.ID,
				Actor: peer.Name, At: msg.At,
			})
			res.Changes = append(res.Changes, c.Op+": "+c.Detail)
		}

		if existing {
			err = g.Store.UpdatePNR(ctx, rec, expected, events)
		} else {
			rec.CreatedAt = msg.At
			if rec.RecordLocator == "" {
				if rec.RecordLocator, err = g.newLocator(ctx); err != nil {
					return err
				}
			}
			err = g.Store.CreatePNR(ctx, rec, events)
		}
		switch {
		case err == nil:
			msg.PNRID = rec.ID
			msg.Status = store.StatusApplied
			res.PNRID = rec.ID
			res.Locator = rec.RecordLocator
			res.Status = store.StatusApplied
			g.Bus.Publish(EvPNR, g.pnrView(rec))
			g.trace(msg.ID, "applied", fmt.Sprintf("%s v%d, %d change(s)",
				rec.RecordLocator, rec.Version, len(changes)))
			return g.respond(ctx, peer, msg, dec, rec, res, opts)

		case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrDuplicate):
			// Another writer got there first. Re-read and reapply: the message
			// is still valid, it just needs to land on the newer state.
			lastErr = err
			g.trace(msg.ID, "retry", "record changed underneath us, reapplying")
			continue

		default:
			return fmt.Errorf("gateway: persist record: %w", err)
		}
	}
	return fmt.Errorf("gateway: gave up applying after %d attempts: %w", maxAttempts, lastErr)
}

// resolveRecord finds the record a message refers to, or returns a new one.
func (g *Gateway) resolveRecord(ctx context.Context, dec *decoded) (*pnr.PNR, bool, error) {
	for _, loc := range dec.Locators {
		if loc == "" {
			continue
		}
		rec, err := g.Store.GetPNR(ctx, loc)
		if err == nil {
			return rec, true, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, false, err
		}
	}
	// A message that answers or amends an existing booking must never be
	// allowed to invent one. Silently creating a record here would turn a
	// locator mismatch -- a real and consequential divergence with a partner --
	// into a phantom booking nobody is watching. Refusing sends it to the dead
	// letter queue instead, where it is visible.
	if !dec.CreatesRecord {
		return nil, false, fmt.Errorf(
			"gateway: %s refers to record locator(s) %v that this system does not hold",
			dec.Kind, dec.Locators)
	}
	return &pnr.PNR{Status: pnr.StatusOpen}, false, nil
}

func (g *Gateway) newLocator(ctx context.Context) (string, error) {
	n, err := g.Store.NextLocatorCounter(ctx)
	if err != nil {
		return "", fmt.Errorf("gateway: allocate locator: %w", err)
	}
	return g.locators.Allocate(n), nil
}

// respond generates and sends a reply when the message requires one.
func (g *Gateway) respond(ctx context.Context, peer *Peer, msg *store.Message, dec *decoded, rec *pnr.PNR, res *Result, opts IngestOptions) error {
	if g.Responder == nil || !dec.NeedsReply {
		return nil
	}
	outcomes, err := g.Responder.Decide(ctx, rec, peer)
	if err != nil {
		return fmt.Errorf("gateway: decide response: %w", err)
	}

	// Record our own decision on our copy before answering, so our state and
	// the answer we give can never disagree.
	events := make([]store.Event, 0, len(outcomes))
	for i := range rec.Segments {
		s := &rec.Segments[i]
		code, ok := outcomes[s.Key()]
		if !ok {
			continue
		}
		if h, isReply := airimp.ReplyTo(airimp.ActionCode(code)); isReply && h != "" {
			s.Status = string(h)
		} else {
			s.Status = code
		}
		events = append(events, store.Event{
			Type: "decided", Detail: s.Describe() + " -> " + code,
			MessageID: msg.ID, Actor: g.Identity.Name, At: msg.At,
		})
	}
	expected := rec.Version
	rec.Recompute()
	rec.UpdatedAt = time.Now().UTC()
	if err := g.Store.UpdatePNR(ctx, rec, expected, events); err != nil {
		return fmt.Errorf("gateway: record decision: %w", err)
	}
	g.Bus.Publish(EvPNR, g.pnrView(rec))

	raw, kind, err := g.buildReply(peer, dec, rec, outcomes)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	if opts.HoldReply {
		// The reply travels back in the same exchange, so record it as sent
		// without handing it to a transport there is no session for.
		outID, err := g.record(ctx, peer, raw, kind, rec.ID, msg.ID, store.StatusSent)
		if err != nil {
			return err
		}
		res.Replies = append(res.Replies, outID)
		res.Reply = raw
		g.trace(msg.ID, "replied inline", kind)
		return nil
	}
	outID, err := g.Send(ctx, peer, raw, kind, rec.ID, msg.ID)
	if err != nil {
		return err
	}
	res.Replies = append(res.Replies, outID)
	g.trace(msg.ID, "replied", kind)
	return nil
}

func (g *Gateway) buildReply(peer *Peer, dec *decoded, rec *pnr.PNR, outcomes map[string]string) ([]byte, string, error) {
	switch dec.Format {
	case store.FormatTypeB:
		codes := map[string]airimp.ActionCode{}
		for k, v := range outcomes {
			codes[airimpKey(k)] = airimp.ActionCode(v)
		}
		text := airimp.BuildReply(dec.Airimp, codes, rec, g.Identity.Designator)
		if text == "" {
			return nil, "", nil
		}
		out := &typeb.Message{
			Priority:     "QU",
			Destinations: []typeb.Address{dec.ReplyTo},
			Origin:       mustAddress(g.Identity.TTYAddress),
			OriginTime:   nowOriginTime(),
			Text:         text,
		}
		raw, err := out.Encode(typeb.EncodeOptions{Charset: typeb.CharsetITA2, CRLF: true})
		return raw, "AIRIMP/reply", err

	case store.FormatEDIFACT:
		ic, err := padis.BuildPAORES(dec.Edifact, rec, outcomes, rec.RecordLocator,
			g.Identity.Designator, padis.BuildOptions{
				Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
				Recipient:  edifact.Party{ID: dec.EdifactSender, Qualifier: "ZZ"},
				ControlRef: nextControlRef(),
				MessageRef: "1",
			})
		if err != nil {
			return nil, "", err
		}
		raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true})
		return raw, padis.MsgPAORES, err
	}
	return nil, "", nil
}

// record writes an outbound message to the log without transmitting it.
func (g *Gateway) record(ctx context.Context, peer *Peer, raw []byte, kind, pnrID, correlationID string, st store.Status) (string, error) {
	now := time.Now().UTC()
	sum := sha256.Sum256(raw)
	out := &store.Message{
		ID: ulid.NewAt(now), Direction: store.Outbound, At: now,
		Transport: "link", Peer: peer.Name, Format: peer.Format, Kind: kind,
		Raw: raw, SHA256: hex.EncodeToString(sum[:]), Size: len(raw),
		Status: st, PNRID: pnrID, CorrelationID: correlationID,
	}
	if err := g.Store.AppendMessage(ctx, out); err != nil {
		return "", fmt.Errorf("gateway: capture outbound: %w", err)
	}
	g.Bus.Publish(EvMessage, g.msgView(out))
	return out.ID, nil
}

// Send records and transmits an outbound message.
//
// Capture precedes transmission for the same reason it does inbound: a message
// that went out must be in the log even if recording what happened to it fails.
func (g *Gateway) Send(ctx context.Context, peer *Peer, raw []byte, kind, pnrID, correlationID string) (string, error) {
	now := time.Now().UTC()
	sum := sha256.Sum256(raw)
	out := &store.Message{
		ID: ulid.NewAt(now), Direction: store.Outbound, At: now,
		Transport: "link", Peer: peer.Name, Format: peer.Format, Kind: kind,
		Raw: raw, SHA256: hex.EncodeToString(sum[:]), Size: len(raw),
		Status: store.StatusSent, PNRID: pnrID, CorrelationID: correlationID,
	}
	if err := g.Store.AppendMessage(ctx, out); err != nil {
		return "", fmt.Errorf("gateway: capture outbound: %w", err)
	}
	if err := g.Sender.Send(ctx, peer.Name, raw); err != nil {
		out.Status = store.StatusUndeliverable
		out.Error = err.Error()
		if uerr := g.Store.UpdateMessage(ctx, out); uerr != nil {
			g.Log.Error("failed to record send failure", "id", out.ID, "err", uerr)
		}
		g.Bus.Publish(EvMessage, g.msgView(out))
		return out.ID, fmt.Errorf("gateway: send to %s: %w", peer.Name, err)
	}
	g.Bus.Publish(EvMessage, g.msgView(out))
	return out.ID, nil
}

// airimpKey converts a canonical segment key to the AIRIMP element key form,
// which omits nothing but is built from the wire date.
func airimpKey(k string) string { return k }

func mustAddress(s string) typeb.Address {
	a, err := typeb.ParseAddress(s)
	if err != nil {
		// Identity is validated at startup; reaching here is a programming
		// error, and a zero address makes it obvious rather than silent.
		return typeb.Address{}
	}
	return a
}

func nowOriginTime() typeb.OriginTime {
	n := time.Now().UTC()
	return typeb.OriginTime{Day: n.Day(), Hour: n.Hour(), Minute: n.Minute(), Present: true}
}

var controlRefMu sync.Mutex
var controlRefSeq int64

// nextControlRef returns an interchange control reference. It only needs to be
// unique per sender, and the sequence restarts on process restart, which is why
// the value is combined with a timestamp component.
func nextControlRef() string {
	controlRefMu.Lock()
	controlRefSeq++
	n := controlRefSeq
	controlRefMu.Unlock()
	return fmt.Sprintf("%d%04d", time.Now().Unix()%100000, n%10000)
}

// msgView renders a message for observers.
//
// node is the designator, not the display name: it is the key the API uses to
// address a specific node's message log, and a label that reads nicely is not
// the same thing as a key that resolves.
func (g *Gateway) msgView(m *store.Message) map[string]any {
	return map[string]any{
		"node": g.Identity.Designator, "node_label": g.Identity.Name, "id": m.ID, "direction": string(m.Direction),
		"at": m.At, "peer": m.Peer, "format": string(m.Format), "kind": m.Kind,
		"status": string(m.Status), "size": m.Size, "error": m.Error,
		"sha256": m.SHA256,
		"pnr_id": m.PNRID, "correlation_id": m.CorrelationID,
		"raw": string(m.Raw), "diagnostics": m.Diagnostics,
	}
}

func (g *Gateway) pnrView(p *pnr.PNR) map[string]any {
	segs := make([]string, 0, len(p.Segments))
	for _, s := range p.Segments {
		segs = append(segs, s.Describe())
	}
	pax := make([]string, 0, len(p.Passengers))
	for _, x := range p.Passengers {
		pax = append(pax, x.Display())
	}
	return map[string]any{
		"node": g.Identity.Designator, "node_label": g.Identity.Name,
		"id": p.ID, "locator": p.RecordLocator, "version": p.Version,
		"status": string(p.Status), "segments": segs, "passengers": pax,
		"updated_at": p.UpdatedAt, "unparsed": len(p.Unparsed),
		"record": p.Redacted(),
	}
}

// TrimForLog shortens a raw message for a log line.
func TrimForLog(raw []byte, n int) string {
	s := strings.ReplaceAll(string(raw), "\n", "\\n")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
