package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/acars"
	"github.com/adamf/jetway/pkg/aftn"
	"github.com/adamf/jetway/pkg/airimp"
	"github.com/adamf/jetway/pkg/ats"
	"github.com/adamf/jetway/pkg/avs"
	"github.com/adamf/jetway/pkg/baggage"
	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/mvt"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnl"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/ssim"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/typeb"
)

// change is one effect a message had, normalised across codecs.
type change struct{ Op, Detail string }

// decoded is a message that has been classified and parsed but not yet applied.
type decoded struct {
	Format      store.Format
	Kind        string
	DedupKey    string
	Diagnostics []store.Diagnostic

	// PossibleDuplicate carries the Type B PDM indicator through to the
	// deduplication step, where it decides whether a repeat is expected.
	PossibleDuplicate bool

	// Locators are the record locators the message refers to, in the order to
	// try them when finding the record it belongs to.
	Locators []string

	// Movement is a parsed MVT/MVA/DIV, when the message is one.
	Movement *mvt.Message

	// NeedsReply reports that the sender is waiting for an answer.
	NeedsReply bool
	// CreatesRecord reports that this message class may open a new booking.
	// Replies and amendments may not: for those, a locator that matches nothing
	// is a divergence to investigate, not a new record to create.
	CreatesRecord bool
	// Test reports that the sender marked this as test traffic.
	Test bool

	TypeB  *typeb.Message
	Airimp *airimp.Message
	// AVS is set when the message carries availability rather than a booking.
	AVS *avs.Message
	// NameList is set when the message is a PNL or ADL: a list for an
	// airport, not an amendment to any one booking.
	NameList *pnl.Message
	// Baggage is set when the message is a BSM or BPM: about a bag, not a
	// booking.
	Baggage *baggage.Message
	// Departure is set when the message is departure control output -- PFS,
	// PTM, PSM, ETL, LDM, CPM: about a flight that has closed, not a booking.
	Departure *dcs.Message
	// AFTN is the envelope when the message came over the aeronautical
	// fixed network; ATS is the air traffic services message inside it,
	// when the text is one.
	AFTN *aftn.Message
	ATS  *ats.Message
	// OOOI is an aircraft's datalink report -- out, off, on, in -- forwarded
	// by the service provider over Type B.
	OOOI *acars.Message
	// Unreadable is set when a message was recognised by its identifier --
	// a movement, a name list, a bag message, departure output -- and then
	// failed to parse. It goes to the dead letter queue with this reason,
	// rather than falling through to the booking grammar and being refused
	// for not naming a record it was never about.
	Unreadable string
	// Schedule is set when the message is an SSM or ASM. Like availability, a
	// schedule change touches no single record and must not create one.
	Schedule *ssim.Message
	ReplyTo  typeb.Address

	Edifact       edifact.Message
	EdifactSender string
	// Interchange is the whole decoded envelope, kept because a CONTRL reports
	// on the interchange rather than on the message inside it.
	Interchange *edifact.Interchange
	// CONTRL is set when the inbound message is itself a syntax and service
	// report, in which case it says what a partner made of something we sent.
	CONTRL *edifact.Report
	// TicketControl is set for TKCREQ and TKCRES: what a partner says has
	// become of a coupon on a document.
	TicketControl *padis.TicketControl

	peer *Peer
	// self is the receiving node's designator.
	self string
}

// applyTo folds the decoded message into a record.
func (d *decoded) applyTo(rec *pnr.PNR, peer *Peer, at time.Time) []change {
	var out []change
	switch d.Format {
	case store.FormatTypeB:
		if d.Airimp == nil {
			return nil
		}
		for _, c := range airimp.Apply(rec, d.Airimp, airimp.ApplyOptions{
			ReceivedAt: at, Party: peer.Carrier, Inbound: true, Self: d.self,
		}) {
			out = append(out, change{c.Op, c.Detail})
		}
		if rec.Origin.Party == "" {
			rec.Origin.Party = peer.Carrier
			rec.Origin.Channel = "typeb"
		}
	case store.FormatEDIFACT:
		for _, c := range peer.padis().Apply(rec, d.Edifact, padis.ApplyOptions{
			ReceivedAt: at, Party: d.EdifactSender, Inbound: true, Self: d.self,
		}) {
			out = append(out, change{c.Op, c.Detail})
		}
		if rec.Origin.Party == "" {
			rec.Origin.Party = d.EdifactSender
			rec.Origin.Channel = "edifact"
		}
	}
	return out
}

// looksLikeEDIFACT reports whether the bytes carry an EDIFACT interchange.
//
// Classification is by content, not by link configuration. A link that is
// configured as teletype but starts carrying EDIFACT should be processed, and
// noticed, rather than mangled by the wrong decoder.
func looksLikeEDIFACT(raw []byte) bool {
	head := raw
	if len(head) > 512 {
		head = head[:512]
	}
	trimmed := bytes.TrimLeft(head, " \r\n\t")
	return bytes.HasPrefix(trimmed, []byte("UNA")) ||
		bytes.HasPrefix(trimmed, []byte("UNB")) ||
		bytes.Contains(head, []byte("UNB+"))
}

// decode classifies and parses a captured message.
func (g *Gateway) decode(peer *Peer, msg *store.Message, opts IngestOptions) (*decoded, error) {
	raw := msg.Raw
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("gateway: empty message")
	}
	var (
		d   *decoded
		err error
	)
	switch {
	case looksLikeEDIFACT(raw):
		d, err = g.decodeEDIFACT(peer, raw, opts)
	case aftn.Looks(raw):
		d, err = g.decodeAFTN(peer, raw)
	default:
		d, err = g.decodeTypeB(peer, raw)
	}
	if d != nil {
		d.self = g.Identity.Designator
	}
	return d, err
}

func (g *Gateway) decodeEDIFACT(peer *Peer, raw []byte, opts IngestOptions) (*decoded, error) {
	// A file dropped by an ERP or a transmission system often wraps the
	// interchange in a vendor header. Those bytes are read once and never
	// relayed, so skipping a preamble is safe there and not on a live link.
	ic, err := edifact.Parse(raw, edifact.ParseOptions{SkipPreamble: opts.FromFile})
	if err != nil {
		return nil, fmt.Errorf("gateway: edifact decode: %w", err)
	}
	d := &decoded{Format: store.FormatEDIFACT, peer: peer}
	for _, x := range ic.Diagnostics {
		d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
			Layer: "edifact", Severity: x.Severity.String(), Code: x.Code, Detail: x.Detail,
		})
	}
	if len(ic.Messages) == 0 {
		return nil, fmt.Errorf("gateway: edifact interchange carries no messages")
	}
	// One message per interchange is the norm for interactive reservation
	// traffic. Batches exist; processing the first and recording the rest as a
	// diagnostic is honest about what this build does.
	if len(ic.Messages) > 1 {
		d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
			Layer: "edifact", Severity: "warn", Code: "multi_message_interchange",
			Detail: fmt.Sprintf("interchange carries %d messages; only the first was applied", len(ic.Messages)),
		})
	}
	d.Edifact = ic.Messages[0]
	d.EdifactSender = ic.Sender().ID
	d.Kind = d.Edifact.ID().Type
	d.Test = ic.TestIndicator()
	d.Interchange = ic

	// Ticket control is about a document, not a booking, so it branches before
	// the record grammar. Feeding it through would produce a message of
	// unrecognised segments and touch the wrong thing.
	if padis.IsTicketControl(d.Edifact) {
		tc, err := padis.ParseTicketControl(d.Edifact)
		if err != nil {
			return nil, fmt.Errorf("gateway: %w", err)
		}
		d.TicketControl = tc
		d.NeedsReply = !tc.Response
		return d, nil
	}

	// A CONTRL is about an interchange we sent, so it touches no record and
	// must not be run through the record grammar.
	if edifact.IsCONTRL(d.Edifact) {
		rep, err := edifact.ParseCONTRL(d.Edifact)
		if err != nil {
			return nil, fmt.Errorf("gateway: %w", err)
		}
		d.CONTRL = rep
		return d, nil
	}

	// The interchange control reference is the sender's own idempotency key and
	// is exactly what a retransmission repeats.
	if ref := ic.ControlRef(); ref != "" {
		d.DedupKey = "unb:" + ref
	}

	for _, seg := range d.Edifact.Find("RCI") {
		for i := range seg.Elements {
			for _, c := range seg.Elem(i) {
				if loc := c.Get(1); loc != "" {
					d.Locators = append(d.Locators, loc)
				}
			}
		}
	}
	// A PAOREQ whose function code says cancellation is an advisory. The
	// sender has already cancelled; answering it as if it were a sell request
	// is how a cancelled booking used to get refused back to life.
	d.NeedsReply = d.Kind == padis.MsgPAOREQ &&
		padis.MessageFunction(d.Edifact) != padis.FuncCancellation
	d.CreatesRecord = d.Kind == padis.MsgPAOREQ
	return d, nil
}

// decodeAFTN reads an aeronautical fixed network message. The envelope is
// specified by ICAO Annex 10; the text is an ATS message when it looks like
// one, and free text otherwise.
func (g *Gateway) decodeAFTN(peer *Peer, raw []byte) (*decoded, error) {
	env, err := aftn.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("gateway: aftn decode: %w", err)
	}
	d := &decoded{Format: store.FormatAFTN, AFTN: env, peer: peer, Kind: "AFTN/" + string(env.Priority)}
	if !ats.Looks(env.Text) {
		return d, nil
	}
	m, err := ats.Parse(env.Text)
	if err != nil {
		d.Kind = "ATS"
		d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
			Layer: "ats", Severity: "warn", Code: "unreadable_ats_message", Detail: err.Error(),
		})
		d.Unreadable = err.Error()
		return d, nil
	}
	d.ATS = m
	d.Kind = "ATS/" + string(m.Type) + "/" + m.AircraftID
	for _, u := range m.Unparsed {
		d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
			Layer: "ats", Severity: "info", Code: "unparsed_field", Detail: u,
		})
	}
	return d, nil
}

func (g *Gateway) decodeTypeB(peer *Peer, raw []byte) (*decoded, error) {
	tb, err := typeb.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("gateway: type b decode: %w", err)
	}
	d := &decoded{Format: store.FormatTypeB, TypeB: tb, ReplyTo: tb.Origin, peer: peer}
	for _, x := range tb.Diagnostics {
		d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
			Layer: "typeb", Severity: x.Severity.String(), Code: x.Code,
			Detail: x.Detail, Line: x.Line,
		})
	}
	if tb.Text == "" {
		return nil, fmt.Errorf("gateway: type b message has no text")
	}

	// Schedule messages are Type B and say nothing about any one booking, so
	// like availability they branch before the reservation grammar rather than
	// being fed to it and producing a message full of unrecognised elements.
	if ssim.IsSchedule(tb.Text) {
		sm, err := peer.ssim().Parse(tb.Text)
		if err != nil {
			return nil, fmt.Errorf("gateway: schedule decode: %w", err)
		}
		d.Schedule = sm
		d.Kind = string(sm.Kind) + "/" + string(sm.Action)
		for _, x := range sm.Diagnostics {
			d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
				Layer: "ssim", Severity: x.Severity.String(), Code: x.Code,
				Detail: x.Detail, Line: x.Line,
			})
		}
		return d, nil
	}

	// Movement messages are about an aircraft, not a booking, so they too
	// branch before the reservation grammar. A parse failure is a diagnostic
	// rather than a rejection: the bytes are already captured, and an MVT a
	// profile cannot read is evidence, not garbage.
	if mvt.IsMovement(tb.Text) {
		mm, err := mvt.Parse(tb.Text)
		if err != nil {
			d.Kind = "MVT"
			d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
				Layer: "mvt", Severity: "warn", Code: "unreadable_movement",
				Detail: err.Error(),
			})
			d.Unreadable = err.Error()
			return d, nil
		}
		d.Movement = mm
		d.Kind = string(mm.Kind) + "/" + mm.Flight
		for _, u := range mm.Unrecognised {
			d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
				Layer: "mvt", Severity: "info", Code: "unrecognised_line", Detail: u,
			})
		}
		return d, nil
	}

	// An aircraft's datalink report, forwarded by the provider. About an
	// airframe, not a booking, and read by operations.
	if acars.IsOOOI(tb.Text) {
		om, err := acars.Parse(tb.Text)
		if err != nil {
			d.Kind = firstWord(tb.Text)
			d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
				Layer: "acars", Severity: "warn", Code: "unreadable_datalink_report",
				Detail: err.Error(),
			})
			d.Unreadable = err.Error()
			return d, nil
		}
		d.OOOI = om
		d.Kind = "ACARS/" + string(om.Kind) + "/" + om.Flight
		return d, nil
	}

	// Name lists are addressed to an airport about a departure, not to a
	// reservation system about a booking; they classify and file, and feed
	// nothing to the record grammar.
	if pnl.IsNameList(tb.Text) {
		nm, err := pnl.Parse(tb.Text)
		if err != nil {
			d.Kind = firstWord(tb.Text)
			d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
				Layer: "pnl", Severity: "warn", Code: "unreadable_name_list",
				Detail: err.Error(),
			})
			d.Unreadable = err.Error()
			return d, nil
		}
		d.NameList = nm
		d.Kind = string(nm.Kind) + "/" + nm.Flight
		return d, nil
	}

	// Departure control output is about a closed flight: final sales, the
	// transfer and service lists, the load. Same rule: classify, file, keep
	// it away from the booking grammar, and hand it to whoever consumes it.
	if dcs.IsDepartureControl(tb.Text) {
		dm, err := dcs.Parse(tb.Text)
		if err != nil {
			d.Kind = firstWord(tb.Text)
			d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
				Layer: "dcs", Severity: "warn", Code: "unreadable_departure_message",
				Detail: err.Error(),
			})
			d.Unreadable = err.Error()
			return d, nil
		}
		d.Departure = dm
		d.Kind = string(dm.Kind) + "/" + dm.Flight
		return d, nil
	}

	// Baggage messages are about a bag. Same rule: classify, file, and keep
	// them away from the booking grammar.
	if baggage.IsBaggage(tb.Text) {
		bm, err := baggage.Parse(tb.Text)
		if err != nil {
			d.Kind = firstWord(tb.Text)
			d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
				Layer: "baggage", Severity: "warn", Code: "unreadable_bag_message",
				Detail: err.Error(),
			})
			d.Unreadable = err.Error()
			return d, nil
		}
		d.Baggage = bm
		d.Kind = string(bm.Kind)
		if bm.Outbound != nil {
			d.Kind += "/" + bm.Outbound.Flight
		}
		return d, nil
	}

	// Availability messages are Type B but say nothing about any booking, so
	// they branch before the reservation grammar rather than being fed to it
	// and producing a message full of unrecognised elements.
	if avs.IsAvailability(tb.Text) {
		am := peer.avs().Parse(tb.Text, g.msgTime(peer))
		d.AVS = am
		d.Kind = "AVS"
		for _, x := range am.Diagnostics {
			d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
				Layer: "avs", Severity: x.Severity.String(), Code: x.Code,
				Detail: x.Detail, Line: x.Line,
			})
		}
		return d, nil
	}

	am := peer.airimp().Parse(tb.Text)
	d.Airimp = am
	for _, x := range am.Diagnostics {
		d.Diagnostics = append(d.Diagnostics, store.Diagnostic{
			Layer: "airimp", Severity: x.Severity.String(), Code: x.Code,
			Detail: x.Detail, Line: x.Line,
		})
	}
	d.Kind = "AIRIMP/" + string(am.Intent())
	d.NeedsReply = am.Intent() == airimp.IntentSell
	d.CreatesRecord = am.Intent() == airimp.IntentSell

	for _, l := range am.Locators() {
		d.Locators = append(d.Locators, l.Value)
	}
	d.DedupKey = typeBDedupKey(tb)
	d.PossibleDuplicate = tb.PossibleDuplicate

	// A reply must go back to the originator. Fall back to the configured
	// address so a message with a damaged origin line is still answerable.
	if d.ReplyTo.IsZero() && peer.TTYAddress != "" {
		d.ReplyTo = mustAddress(peer.TTYAddress)
	}
	return d, nil
}

// typeBDedupKey derives an idempotency key for a teletype message.
//
// Type B carries no mandatory message identifier, so there is nothing as clean
// as an EDIFACT control reference to key on. The key here combines the
// originator, the origin time group and a digest of the text: the same text,
// from the same sender, stamped with the same minute is a retransmission in
// every practical case.
//
// The limitation is real and worth stating. Two genuinely distinct messages
// that are byte-identical and stamped within the same minute will be treated as
// one. For a sell request that is the safer error -- booking the same seats
// twice is worse than declining a repeat -- but a link whose traffic is
// legitimately repetitive should carry a sender-supplied reference instead, and
// the key should be configured to use it.
func typeBDedupKey(tb *typeb.Message) string {
	if tb.Origin.IsZero() || !tb.OriginTime.Present {
		return ""
	}
	// A carrier-supplied sequence number on the origin line is a better key
	// than a digest, so prefer it when one is present.
	for _, extra := range tb.OriginExtra {
		if len(extra) >= 3 && strings.IndexFunc(extra, func(r rune) bool {
			return r < '0' || r > '9'
		}) < 0 {
			return "tty:" + tb.Origin.String() + ":" + extra
		}
	}
	sum := sha256.Sum256([]byte(tb.Text))
	return "tty:" + tb.Origin.String() + ":" + tb.OriginTime.String() + ":" + hex.EncodeToString(sum[:8])
}

// msgTime anchors date resolution through the gateway's clock seam, so a
// simulation driving time gets consistent date windows too.
func (g *Gateway) msgTime(*Peer) time.Time { return g.now() }

func firstWord(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		if f := strings.Fields(ln); len(f) > 0 {
			return f[0]
		}
	}
	return "typeb"
}
