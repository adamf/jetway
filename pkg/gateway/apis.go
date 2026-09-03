package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/paxlst"
	"github.com/adamf/jetway/pkg/store"
)

// APISOptions shape the passenger list a closed flight produces for a
// border control agency.
type APISOptions struct {
	// Departs and Arrives are the sector's local times.
	Departs, Arrives time.Time
	// Function is the BGM identifier: paxlst.FuncCloseOnBoard for the list
	// sent at the door, empty for a plain list.
	Function string
	// Contact is the party responsible for the message content, a profile
	// identifier assigned by the agency, or a name.
	Contact, ContactSurname, ContactGiven string
	// TxnRef is the transaction reference the airline assigns.
	TxnRef string
	// OnBoardOnly reports boarded passengers only (the flight-close list);
	// otherwise everyone accepted.
	OnBoardOnly bool
}

// APISFor builds the passenger list for a closed flight from what departure
// control holds: names as accepted, the seat and sequence, bags and tags,
// and the travel document when the record carried an SSR DOCS. A passenger
// with no document is reported by name with the information marked not
// verified, which is what the agency then asks about.
func APISFor(fl *dcs.Flight, opts APISOptions) *paxlst.Message {
	m := &paxlst.Message{
		Ref: "PAX001", List: paxlst.ListPassengers, Function: opts.Function, TxnRef: opts.TxnRef,
		Contact: opts.Contact, ContactSurname: opts.ContactSurname, ContactGiven: opts.ContactGiven,
		Legs: []paxlst.Leg{{Carrier: fl.Flight[:min(2, len(fl.Flight))], Number: strings.TrimPrefix(fl.Flight, fl.Flight[:min(2, len(fl.Flight))]),
			From: fl.Board, To: fl.Dest, Departs: opts.Departs, DepartsHasTime: !opts.Departs.IsZero(),
			Arrives: opts.Arrives, ArrivesHasTime: !opts.Arrives.IsZero()}},
	}
	for _, p := range fl.Passengers {
		if opts.OnBoardOnly && p.Status != dcs.StatusBoarded {
			continue
		}
		if !opts.OnBoardOnly && p.Status != dcs.StatusBoarded && p.Status != dcs.StatusAccepted {
			continue
		}
		person := paxlst.Person{Party: paxlst.PartyPassenger, Surname: p.Surname, Given: givenOnly(p.Given),
			Seat: p.Seat, Locator: p.Locator, PassengerRef: fmt.Sprintf("%s%03d", fl.Flight, p.Sequence),
			Embarked: fl.Board, Destination: p.Dest, Clearance: fl.Dest}
		if person.Destination == "" {
			person.Destination = fl.Dest
		}
		verified := false
		for _, s := range p.SSRs {
			if s.Code != "DOCS" {
				continue
			}
			if d, ok := paxlst.ParseDOCS(s.Text); ok {
				person.Documents = append(person.Documents, paxlst.Document{Type: d.Type, Number: d.Number, Issuer: d.Issuer, Expires: d.Expires})
				person.Nationality, person.Gender, person.DateOfBirth = d.Nationality, d.Gender, d.DateOfBirth
				if d.Surname != "" {
					person.Surname = d.Surname
				}
				if d.Given != "" {
					person.Given = d.Given
				}
				verified = true
			}
		}
		person.Verified = &verified
		if n := len(p.Bags); n > 0 {
			person.Bags = n
			for _, b := range p.Bags {
				person.BagWeightKg += b.Weight
				person.BagTags = append(person.BagTags, paxlst.BagTag{Number: b.Tag, Count: 1})
			}
		}
		m.People = append(m.People, person)
	}
	m.Total = len(m.People)
	return m
}

// givenOnly strips the title a name list runs onto the given name (JOHNMR),
// which a travel document does not carry.
func givenOnly(given string) string {
	for _, t := range []string{"MRS", "MSTR", "MISS", "MR", "MS", "DR", "INF", "CHD"} {
		if len(given) > len(t) && strings.HasSuffix(given, t) {
			return strings.TrimSuffix(given, t)
		}
	}
	return given
}

// SendAPIS transmits a passenger list to a border control agency's peer over
// EDIFACT: the flight-close list, or an update.
func (g *Gateway) SendAPIS(ctx context.Context, peer *Peer, m *paxlst.Message) error {
	to := peer.Carrier
	if to == "" {
		to = peer.Name
	}
	ic, err := paxlst.Build(m, paxlst.BuildOptions{
		Sender: edifact.Party{ID: g.Identity.Designator}, Recipient: edifact.Party{ID: to},
		ControlRef: g.nextControlRef(), Now: g.now(), Group: true,
	})
	if err != nil {
		return fmt.Errorf("gateway: build PAXLST: %w", err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		return fmt.Errorf("gateway: encode PAXLST: %w", err)
	}
	_, err = g.SendKeyed(ctx, peer, raw, paxlst.MsgPAXLST, "", "", "")
	return err
}

// applyPAXLST is a border agency's side: a passenger list arrived; whoever
// runs this node as an agency hears it.
func (g *Gateway) applyPAXLST(ctx context.Context, peer *Peer, msg *store.Message, dec *decoded, res *Result) error {
	msg.Kind = paxlst.MsgPAXLST
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	g.trace(msg.ID, "apis", dec.PAXLST.Describe())
	if g.APIS != nil {
		g.APIS(ctx, peer, dec.PAXLST)
	}
	return nil
}
