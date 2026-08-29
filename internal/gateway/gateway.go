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

	"github.com/adamf/jetway/internal/metrics"
	"github.com/adamf/jetway/internal/queue"
	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/internal/ulid"
	"github.com/adamf/jetway/pkg/airimp"
	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/avs"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/ssim"
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
	// SequenceWrap is the number this link's channel counter returns to after
	// its highest value. Zero means unknown, and a rollover then reads as a
	// large gap rather than being silently absorbed.
	SequenceWrap int

	// CONTRL is when to send a syntax and service report for an EDITFACT
	// interchange this peer sends: "requested" (the default) honours the
	// acknowledgement request in UNB 0031, "always", "errors" reports only
	// rejections, and "never" sends none.
	CONTRL string

	// TTYAddress is the peer's Type B address, used when Format is typeb.
	TTYAddress string
	// Addresses are further Type B addresses this link serves, beyond
	// TTYAddress. A carrier commonly has one address per department and one
	// circuit, so routing on the address needs the whole set.
	Addresses []string
	// AirimpProfile overrides the AIRIMP grammar for this link. Nil uses the
	// default profile.
	AirimpProfile *airimp.Profile
	// PadisProfile overrides the PADIS segment handlers for this link.
	PadisProfile *padis.Profile
	// SSIMProfile overrides the SSM and ASM grammar for this link.
	SSIMProfile *ssim.Profile
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

// SSIMProfile overrides the schedule message grammar for this link.
func (p *Peer) ssim() *ssim.Profile {
	if p.SSIMProfile != nil {
		return p.SSIMProfile
	}
	return ssim.Default
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

	// Queues turns partner answers into work. Nil means this node keeps no
	// queues, which is right for a simulated carrier and wrong for a GDS.
	Queues *queue.Manager

	// ScheduleScanLimit bounds the record scan a schedule message triggers.
	// Zero uses defaultScheduleScanLimit.
	ScheduleScanLimit int

	// seq tracks the last channel sequence number seen per link, so a gap in a
	// partner's numbering is visible. It is in memory on purpose: a restart
	// loses the baseline, and inventing continuity across one would report a
	// gap that is really just this process not having been running.
	seqMu sync.Mutex
	seq   map[string]int

	locators *pnr.LocatorAllocator

	// Relay makes the gateway forward messages addressed to peers other than
	// itself. It is off by default and should stay off unless the deployment is
	// meant to be a switch: a node that relays on behalf of anyone who can
	// reach it is an open relay, spending someone else's link budget under our
	// own originator address.
	Relay bool

	mu    sync.RWMutex
	peers map[string]*Peer
	// byCarrier routes an outbound message for a carrier to the link that
	// serves it.
	byCarrier map[string]*Peer
	// byAddress routes on the Type B address line, which is how a message with
	// several addressees reaches all of them.
	byAddress map[string]*Peer
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
		byAddress: map[string]*Peer{},
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
	for _, a := range append([]string{p.TTYAddress}, p.Addresses...) {
		if a = normaliseAddress(a); a != "" {
			g.byAddress[a] = p
		}
	}
}

// normaliseAddress upper-cases and trims a TTY address for use as a map key.
func normaliseAddress(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// PeerByAddress resolves a Type B address to the link that serves it.
func (g *Gateway) PeerByAddress(addr string) *Peer {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.byAddress[normaliseAddress(addr)]
}

// IsSelf reports whether an address belongs to this node.
func (g *Gateway) IsSelf(addr string) bool {
	a := normaliseAddress(addr)
	return a != "" && a == normaliseAddress(g.Identity.TTYAddress)
}

// Delivery is what happened to one addressee of a fanned-out message.
type Delivery struct {
	Address string `json:"address"`
	// Peer is the link the address resolved to, empty when it did not resolve.
	Peer string `json:"peer,omitempty"`
	// MessageID is the outbound log entry, empty when nothing was sent.
	MessageID string `json:"message_id,omitempty"`
	// Self reports that the address is this node, so the message terminates
	// here rather than being forwarded.
	Self bool `json:"self,omitempty"`
	// Err is why an addressee was not reached.
	Err string `json:"error,omitempty"`
}

// Fanout delivers one message to every address on its priority line.
//
// A Type B message may carry several addressees and the network is expected to
// deliver a copy to each. Routing on the configured peer name alone cannot do
// that: it can only ever reach the one link a message was handed to.
//
// The bytes are sent unchanged. Rewriting the address line per recipient would
// make each copy a different message from the one in the log, and the address
// line is part of what a partner may check.
func (g *Gateway) Fanout(ctx context.Context, tb *typeb.Message, raw []byte,
	kind, pnrID, correlationID string) []Delivery {
	out := make([]Delivery, 0, len(tb.Destinations))
	for _, d := range tb.Destinations {
		addr := d.String()
		switch {
		case g.IsSelf(addr):
			out = append(out, Delivery{Address: addr, Self: true})
			continue
		default:
		}
		peer := g.PeerByAddress(addr)
		if peer == nil {
			// Not routable here. Recorded rather than dropped: on a real
			// network this is the point where a message would go to the
			// switch's undeliverable queue for an operator to look at.
			out = append(out, Delivery{Address: addr, Err: "no link serves this address"})
			continue
		}
		id, err := g.Send(ctx, peer, raw, kind, pnrID, correlationID)
		del := Delivery{Address: addr, Peer: peer.Name, MessageID: id}
		if err != nil {
			del.Err = err.Error()
		}
		out = append(out, del)
	}
	return out
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
	msg.PossibleDuplicate = dec.PossibleDuplicate
	g.checkSequence(peer, msg, dec)
	msg.Diagnostics = dec.Diagnostics
	msg.Status = store.StatusDecoded
	g.trace(msg.ID, "decoded", string(dec.Format)+" "+dec.Kind)

	// A retransmission is normal on a store-and-forward link. Record it, then
	// decline to apply it: applying a sell twice books two seats.
	if dec.DedupKey != "" {
		if prev, seen, err := g.Store.FindByDedupKey(ctx, peer.Name, dec.DedupKey); err == nil && seen && prev != msg.ID {
			msg.Status = store.StatusRejected
			msg.CorrelationID = prev
			res.Duplicate = true
			res.Status = store.StatusRejected
			// A repeat the sender flagged PDM is the store-and-forward network
			// behaving as designed. An unflagged one means the sender believes
			// this is a new instruction, so say so differently: the bytes are
			// suppressed either way, but only one of them is a divergence.
			if dec.PossibleDuplicate {
				msg.Error = "retransmission of " + prev + ", marked PDM by the sender"
				g.trace(msg.ID, "duplicate", "sender marked PDM; already processed as "+prev)
			} else {
				msg.Error = "duplicate of " + prev + ", not marked PDM"
				g.trace(msg.ID, "duplicate", "already processed as "+prev+" and not marked PDM")
			}
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

	// Transit traffic: addresses on this message that belong to other links.
	if g.Relay && dec.TypeB != nil && len(dec.TypeB.Destinations) > 0 {
		if done := g.relay(ctx, peer, msg, dec, res); done {
			return nil
		}
	}

	// A partner telling us what they made of something we sent.
	if dec.CONTRL != nil {
		return g.applyCONTRL(ctx, peer, msg, dec, res)
	}

	// Report on the syntax before acting on the content: the two are separate
	// answers, and a partner is owed the first even when the second fails.
	g.sendCONTRL(ctx, peer, msg, dec)

	if dec.Schedule != nil {
		return g.applySchedule(ctx, peer, msg, dec, res)
	}

	if dec.AVS != nil {
		return g.applyAvailability(ctx, msg, dec, res)
	}
	return g.apply(ctx, peer, msg, dec, res, opts)
}

// relay forwards a message to the addressees that are not this node, and
// reports whether the message was pure transit and so needs no further
// processing here.
//
// The address that matters most is the one we skip. Forwarding to the link the
// message arrived on sends it straight back to its sender, and on a
// store-and-forward network that is a loop that survives restarts.
func (g *Gateway) relay(ctx context.Context, from *Peer, msg *store.Message, dec *decoded, res *Result) bool {
	forUs := false
	var onward []typeb.Address
	for _, d := range dec.TypeB.Destinations {
		addr := d.String()
		if g.IsSelf(addr) {
			forUs = true
			continue
		}
		if p := g.PeerByAddress(addr); p != nil && p.Name == from.Name {
			// Addressed to the sender's own link. Never send it back.
			g.trace(msg.ID, "relay", "skipping "+addr+": it is the link this arrived on")
			continue
		}
		onward = append(onward, d)
	}
	if len(onward) == 0 {
		return false
	}

	fan := *dec.TypeB
	fan.Destinations = onward
	deliveries := g.Fanout(ctx, &fan, msg.Raw, "relay", "", msg.ID)
	sent := 0
	for _, d := range deliveries {
		if d.Err == "" && d.MessageID != "" {
			sent++
			res.Replies = append(res.Replies, d.MessageID)
			continue
		}
		g.trace(msg.ID, "relay", d.Address+": "+d.Err)
	}
	g.trace(msg.ID, "relay", fmt.Sprintf("forwarded to %d of %d addressees", sent, len(onward)))

	if forUs {
		// We are an addressee too, so the message is ours to apply as well.
		return false
	}
	// Pure transit. Nothing here to apply, and inventing a record from a
	// message addressed to somebody else would be worse than doing nothing.
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	if sent == 0 {
		msg.Status = store.StatusUndeliverable
		msg.Error = "addressed elsewhere and no addressee could be routed"
		res.Status = store.StatusUndeliverable
	}
	return true
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
		// Snapshot before applying: a placement is driven by what changed, not
		// by what the record happens to say afterwards. Without this, any later
		// message touching a confirmed record would re-raise the confirmation.
		before := segmentStatuses(rec)
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
			g.enqueueStatusChanges(ctx, rec, before, msg)
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

	raw, kind, key, err := g.buildReply(peer, dec, rec, outcomes)
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
	outID, err := g.SendKeyed(ctx, peer, raw, kind, rec.ID, msg.ID, key)
	if err != nil {
		return err
	}
	res.Replies = append(res.Replies, outID)
	g.trace(msg.ID, "replied", kind)
	return nil
}

func (g *Gateway) buildReply(peer *Peer, dec *decoded, rec *pnr.PNR, outcomes map[string]string) ([]byte, string, string, error) {
	switch dec.Format {
	case store.FormatTypeB:
		codes := map[string]airimp.ActionCode{}
		for k, v := range outcomes {
			codes[airimpKey(k)] = airimp.ActionCode(v)
		}
		text := airimp.BuildReply(dec.Airimp, codes, rec, g.Identity.Designator)
		if text == "" {
			return nil, "", "", nil
		}
		out := &typeb.Message{
			Priority:     "QU",
			Destinations: []typeb.Address{dec.ReplyTo},
			Origin:       mustAddress(g.Identity.TTYAddress),
			OriginTime:   nowOriginTime(),
			Text:         text,
		}
		raw, err := out.Encode(typeb.EncodeOptions{Charset: typeb.CharsetITA2, CRLF: true})
		return raw, "AIRIMP/reply", "", err

	case store.FormatEDIFACT:
		ref := nextControlRef()
		ic, err := padis.BuildPAORES(dec.Edifact, rec, outcomes, rec.RecordLocator,
			g.Identity.Designator, padis.BuildOptions{
				Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
				Recipient:  edifact.Party{ID: dec.EdifactSender, Qualifier: "ZZ"},
				ControlRef: ref,
				MessageRef: "1",
			})
		if err != nil {
			return nil, "", "", err
		}
		raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true})
		return raw, padis.MsgPAORES, "unb:" + ref, err
	}
	return nil, "", "", nil
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
	return g.SendKeyed(ctx, peer, raw, kind, pnrID, correlationID, "")
}

// SendKeyed is Send with an application-level key recorded against the message.
//
// For EDIFACT the key is the interchange control reference, which is what a
// partner's CONTRL quotes back. Without it an acknowledgement has nothing to
// match against and can only be filed as a divergence.
func (g *Gateway) SendKeyed(ctx context.Context, peer *Peer, raw []byte, kind, pnrID, correlationID, key string) (string, error) {
	now := time.Now().UTC()
	sum := sha256.Sum256(raw)
	out := &store.Message{
		ID: ulid.NewAt(now), Direction: store.Outbound, At: now,
		Transport: "link", Peer: peer.Name, Format: peer.Format, Kind: kind,
		Raw: raw, SHA256: hex.EncodeToString(sum[:]), Size: len(raw),
		Status: store.StatusSent, PNRID: pnrID, CorrelationID: correlationID,
		DedupKey: key,
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
		// Deliberately no raw body. The stream is a notification channel, and a
		// console that wants the bytes fetches them for the one message it is
		// showing. Broadcasting every payload to every observer made the
		// backlog megabytes and the reconnect expensive.
		"pnr_id": m.PNRID, "correlation_id": m.CorrelationID,
		"diagnostics": m.Diagnostics,
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

// segmentStatuses snapshots segment statuses by segment key.
//
// Keying on the segment rather than its position matters because applying a
// message can add segments and renumber the rest.
func segmentStatuses(p *pnr.PNR) map[string]string {
	out := make(map[string]string, len(p.Segments))
	for i := range p.Segments {
		out[p.Segments[i].Key()] = p.Segments[i].Status
	}
	return out
}

// enqueueStatusChanges places the record on a queue for each segment whose
// status a partner just changed into something a person has to act on.
//
// Failures are logged, never propagated: the message has already been applied
// and acknowledged, and unwinding that because a queue write failed would trade
// a missed task for a lost booking.
func (g *Gateway) enqueueStatusChanges(ctx context.Context, rec *pnr.PNR, before map[string]string, msg *store.Message) {
	if g.Queues == nil {
		return
	}
	for i := range rec.Segments {
		seg := &rec.Segments[i]
		was := before[seg.Key()]
		if seg.Status == was {
			continue
		}
		queueName, code, reason, ok := queue.ForStatus(seg.Status)
		if !ok {
			continue
		}
		if was != "" {
			reason = fmt.Sprintf("%s: %s was %s, now %s", seg.Describe(), reason, was, seg.Status)
		} else {
			reason = fmt.Sprintf("%s: %s", seg.Describe(), reason)
		}
		if _, err := g.Queues.PlaceForSegment(ctx, rec, seg, queueName, code, reason, msg.ID); err != nil {
			g.Log.Error("could not queue a record for action",
				"locator", rec.RecordLocator, "queue", queueName, "err", err)
			continue
		}
		g.trace(msg.ID, "queued", queueName+": "+code)
	}
}

// wantsCONTRL decides whether this peer should get a syntax and service report
// for an interchange they sent.
//
// The default honours the sender's own request in UNB 0031, which is what the
// field is for. Sending one unasked is not wrong, but it is traffic a partner
// did not ask to pay for, so it takes saying so.
func (p *Peer) wantsCONTRL(ic *edifact.Interchange, rejected bool) bool {
	switch p.CONTRL {
	case "never":
		return false
	case "always":
		return true
	case "errors":
		return rejected
	case "", "requested":
		return ic != nil && ic.AckRequested()
	}
	return false
}

// sendCONTRL answers an EDIFACT interchange with a syntax and service report.
//
// Failures here are logged, not propagated. The interchange has already been
// captured and is about to be applied; refusing the whole message because an
// acknowledgement could not be built would turn a reporting problem into a
// lost booking.
func (g *Gateway) sendCONTRL(ctx context.Context, peer *Peer, msg *store.Message, dec *decoded) {
	if dec.Format != store.FormatEDIFACT || dec.Interchange == nil {
		return
	}
	report := edifact.Check(dec.Interchange)
	if !peer.wantsCONTRL(dec.Interchange, report.Rejected()) {
		return
	}
	// A CONTRL travels back the way the interchange came, so the parties swap.
	ic, err := report.Build(edifact.CONTRLOptions{
		Sender:        edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
		Recipient:     edifact.Party{ID: dec.EdifactSender, Qualifier: "ZZ"},
		ControlRef:    nextControlRef(),
		SyntaxVersion: dec.Interchange.Syntax.Version,
		Date:          msg.At.Format("060102"),
		Time:          msg.At.Format("1504"),
	})
	if err != nil {
		g.Log.Error("could not build a syntax and service report", "peer", peer.Name, "err", err)
		return
	}
	raw, err := ic.Encode(edifact.EncodeOptions{})
	if err != nil {
		g.Log.Error("could not encode a syntax and service report", "peer", peer.Name, "err", err)
		return
	}
	if _, err := g.Send(ctx, peer, raw, edifact.MsgCONTRL, "", msg.ID); err != nil {
		g.Log.Error("could not send a syntax and service report", "peer", peer.Name, "err", err)
		return
	}
	g.trace(msg.ID, "contrl", report.Describe())
}

// applyCONTRL records what a partner said about an interchange we sent.
//
// Delivery and acknowledgement are different facts. A transport that accepted
// the bytes proves nothing about whether the partner could read them, and this
// is the only place that difference becomes visible.
func (g *Gateway) applyCONTRL(ctx context.Context, peer *Peer, msg *store.Message, dec *decoded, res *Result) error {
	rep := dec.CONTRL
	msg.Kind = edifact.MsgCONTRL
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	g.trace(msg.ID, "contrl", rep.Describe())

	subject, found, err := g.Store.FindOutboundByKey(ctx, peer.Name, "unb:"+rep.ControlRef)
	if err != nil {
		return fmt.Errorf("gateway: locate the acknowledged interchange: %w", err)
	}
	if !found {
		// A report about something we have no record of sending is a real
		// divergence, not noise: it means our view of the link and theirs
		// disagree about what crossed it.
		msg.Error = "reports on interchange " + rep.ControlRef + ", which this node has no record of sending"
		g.trace(msg.ID, "contrl", msg.Error)
		return nil
	}
	msg.CorrelationID = subject

	out, err := g.Store.GetMessage(ctx, subject)
	if err != nil {
		return fmt.Errorf("gateway: read the acknowledged message: %w", err)
	}
	if rep.Rejected() {
		out.Status = store.StatusRefused
		out.Error = rep.Describe()
	} else {
		out.Status = store.StatusAcknowledged
	}
	if err := g.Store.UpdateMessage(ctx, out); err != nil {
		return fmt.Errorf("gateway: record the acknowledgement: %w", err)
	}
	g.Bus.Publish(EvMessage, g.msgView(out))

	// A refusal is somebody's problem, and nothing else will surface it: the
	// booking looks sent and the partner is not acting on it.
	if rep.Rejected() && g.Queues != nil && out.PNRID != "" {
		if rec, err := g.Store.GetPNRByID(ctx, out.PNRID); err == nil {
			if _, err := g.Queues.Place(ctx, &store.QueueItem{
				Queue: store.QueueDivergence, PNRID: rec.ID, Locator: rec.RecordLocator,
				Code: "contrl_rejected", MessageID: msg.ID,
				Reason: peer.Name + " refused interchange " + rep.ControlRef + ": " + rep.Describe(),
			}); err != nil {
				g.Log.Error("could not queue a refused interchange", "peer", peer.Name, "err", err)
			}
		}
	}
	return nil
}

// applySchedule matches a schedule change against the records that hold the
// flight, and puts each of them in front of somebody.
//
// A schedule message touches no single record, so it never creates one: like
// availability, treating it as a booking update would manufacture a record per
// broadcast. What it does is turn "this flight moved" into "these passengers
// need telling", which is the only reason a distribution system wants the
// message at all.
//
// Matching is a scan over held records. That is honest at these volumes and
// wrong at scale, where the flight key wants an index; ScheduleScanLimit bounds
// it in the meantime.
func (g *Gateway) applySchedule(ctx context.Context, peer *Peer, msg *store.Message, dec *decoded, res *Result) error {
	sm := dec.Schedule
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	g.trace(msg.ID, "schedule", sm.Describe())

	if g.Queues == nil {
		return nil
	}
	if sm.Flight.Carrier == "" {
		return nil
	}
	limit := g.ScheduleScanLimit
	if limit <= 0 {
		limit = defaultScheduleScanLimit
	}
	recs, err := g.Store.ListPNRs(ctx, limit)
	if err != nil {
		return fmt.Errorf("gateway: scan records for a schedule change: %w", err)
	}

	want := sm.Flight.Key()
	placed := 0
	for _, rec := range recs {
		if rec.Status == pnr.StatusCancelled {
			continue
		}
		for i := range rec.Segments {
			seg := &rec.Segments[i]
			if seg.Type != pnr.SegmentAir {
				continue
			}
			if !flightMatches(seg, want, sm) {
				continue
			}
			// The action is part of the reason code, so a later cancellation of
			// a flight already retimed raises a second, distinct task rather
			// than being swallowed as a duplicate of the first.
			code := "schedule_" + strings.ToLower(string(sm.Action))
			reason := fmt.Sprintf("%s: %s", seg.Describe(), sm.Describe())
			ok, err := g.Queues.PlaceForSegment(ctx, rec, seg,
				store.QueueScheduleChange, code, reason, msg.ID)
			if err != nil {
				g.Log.Error("could not queue a schedule change",
					"locator", rec.RecordLocator, "err", err)
				continue
			}
			if ok {
				placed++
			}
		}
	}
	g.trace(msg.ID, "schedule", fmt.Sprintf("%d segment(s) affected", placed))
	g.Bus.Publish(EvTrace, map[string]any{
		"node": g.Identity.Name, "message_id": msg.ID, "step": "schedule",
		"detail": sm.Describe(), "affected": placed,
	})
	return nil
}

// defaultScheduleScanLimit bounds the record scan a schedule change triggers.
const defaultScheduleScanLimit = 1000

// flightMatches reports whether a held segment is on the flight a schedule
// message is about.
//
// The date has to agree as well as the flight. A cancellation for one day must
// not sweep up every passenger booked on that flight number all season, which
// is what matching on the designator alone would do.
func flightMatches(seg *pnr.Segment, flightKey string, sm *ssim.Message) bool {
	if seg.Carrier+strings.TrimLeft(seg.FlightNum, "0") != flightKey {
		return false
	}
	if sm.Period.From == "" {
		// No date stated: the message is about the flight as such, so every
		// holding of it is in scope.
		return true
	}
	if sm.Period.Single() {
		return strings.EqualFold(seg.WireDate, sm.Period.From)
	}
	// A period covers a range this package does not resolve to dates, so the
	// segment is included and the reason names the period. Saying "this may
	// affect you" to a few extra records is the safe direction to be wrong in.
	return true
}

// checkSequence follows a link's channel numbering and records what it says
// about the messages before this one.
//
// This is the only thing that can notice a message that never arrived. Content
// deduplication catches a message sent twice; nothing else catches one sent
// once and lost, because there is no evidence of it anywhere in the pipeline.
// The finding goes on the message as a diagnostic rather than raising an
// error: the message in hand is fine, and it is the hole behind it that needs
// somebody.
func (g *Gateway) checkSequence(peer *Peer, msg *store.Message, dec *decoded) {
	if dec.TypeB == nil || dec.TypeB.Channel == "" {
		return
	}
	channel, seq, ok := typeb.ParseChannel(dec.TypeB.Channel)
	if !ok {
		return
	}
	key := peer.Name + "|" + channel

	g.seqMu.Lock()
	if g.seq == nil {
		g.seq = map[string]int{}
	}
	last, seen := g.seq[key]
	g.seq[key] = seq
	g.seqMu.Unlock()

	if !seen {
		// First message on this channel since start: nothing to compare
		// against, and claiming a gap here would be reporting our own restart.
		return
	}
	gap, differs := typeb.CheckSequence(last, seq, peer.SequenceWrap)
	if !differs {
		return
	}
	switch {
	case gap.Repeat:
		msg.Diagnostics = append(msg.Diagnostics, store.Diagnostic{
			Layer: "typeb", Severity: "warn", Code: "sequence_repeat",
			Detail: fmt.Sprintf("channel %s went back to %d after %d; a retransmission or a sender that restarted its counter",
				channel, seq, last),
		})
		metrics.Counter("jetway_sequence_repeat_total", "channel sequence numbers that went backwards",
			metrics.Labels{"peer": peer.Name})
	default:
		msg.Diagnostics = append(msg.Diagnostics, store.Diagnostic{
			Layer: "typeb", Severity: "error", Code: "sequence_gap",
			Detail: fmt.Sprintf("channel %s jumped from %d to %d; %d message(s) never arrived",
				channel, last, seq, gap.Missing),
		})
		metrics.Counter("jetway_sequence_gap_total", "messages missing from a link's channel numbering",
			metrics.Labels{"peer": peer.Name})
		g.Log.Error("a link's numbering skipped messages",
			"peer", peer.Name, "channel", channel, "expected", gap.Expected,
			"got", seq, "missing", gap.Missing)
		g.trace(msg.ID, "sequence", fmt.Sprintf("%d message(s) missing on channel %s", gap.Missing, channel))
	}
}
