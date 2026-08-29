// Package ndc implements the IATA New Distribution Capability order messages.
//
// # Scope
//
// Orders, not shopping. OrderCreateRQ, OrderRetrieveRQ, OrderCancelRQ and the
// OrderViewRS that answers them, because an NDC order maps onto the record this
// gateway already keeps. AirShopping and OfferPrice are not here and will not
// be: an offer is a priced thing, pricing needs fares, and fares are out of
// scope for a messaging gateway.
//
// That boundary is workable because an OrderCreateRQ may carry the flights
// inline, in OfferItem/OfferItemType/DetailedFlightItem, rather than only as a
// reference to an offer from a shopping response. A request that names only an
// offer this node never made cannot be resolved, and is refused saying so
// rather than guessed at.
//
// # Version
//
// The EDIST generation, namespace http://www.iata.org/IATA/EDIST, which is what
// the 17.2 and 18.1 schemas use and what most carrier NDC endpoints still
// expose. The 21.3 generation renamed the messages to IATA_OrderCreateRQ and
// restructured the payload; it is a different mapping and is not implemented.
// The schemas are published, so unlike the teletype side this can be checked.
//
// # Payment
//
// This package refuses payloads carrying card numbers. The gateway's first
// rule is that raw bytes are made durable before anything interprets them, and
// a primary account number must never be written to a message log that has no
// encryption at rest. The two rules cannot both hold, so the payload is
// rejected at the door rather than quietly stored.
package ndc

import (
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// Namespace is the EDIST namespace all these messages live in.
const Namespace = "http://www.iata.org/IATA/EDIST"

// Message type names.
const (
	MsgOrderCreateRQ   = "OrderCreateRQ"
	MsgOrderRetrieveRQ = "OrderRetrieveRQ"
	MsgOrderCancelRQ   = "OrderCancelRQ"
	MsgOrderViewRS     = "OrderViewRS"
	MsgErrorRS         = "ErrorRS"
)

// ErrCardData is returned when a payload carries payment card details.
var ErrCardData = errors.New("ndc: payload carries payment card details, which this gateway will not store")

// ErrOfferOnly is returned when an order references an offer without carrying
// the flights it stands for.
var ErrOfferOnly = errors.New("ndc: order references an offer with no inline flight detail, and this node holds no offers")

// Party identifies who sent the message.
type Party struct {
	AgencyID   string `xml:"Sender>TravelAgencySender>AgencyID"`
	AgencyName string `xml:"Sender>TravelAgencySender>Name"`
	IATANumber string `xml:"Sender>TravelAgencySender>IATA_Number"`
	OtherID    string `xml:"Sender>TravelAgencySender>OtherIDs>OtherID"`
	AirlineID  string `xml:"Sender>AirlineSender>AirlineID"`
}

// ID returns the most specific sender identifier present.
func (p Party) ID() string {
	for _, v := range []string{p.AirlineID, p.IATANumber, p.AgencyID, p.OtherID} {
		if v != "" {
			return v
		}
	}
	return ""
}

// PaxName is a traveller's name.
type PaxName struct {
	Surname string `xml:"Surname"`
	Given   string `xml:"Given"`
	Title   string `xml:"Title"`
}

// Passenger is one traveller in a request.
type Passenger struct {
	ObjectKey string  `xml:"ObjectKey,attr"`
	PTC       string  `xml:"PTC"`
	Name      PaxName `xml:"Name"`
	Email     string  `xml:"Contacts>Contact>EmailContact>Address"`
	Phone     string  `xml:"Contacts>Contact>PhoneContact>Number"`
}

// Flight is one marketed leg inside a detailed flight item.
type Flight struct {
	SegmentKey       string `xml:"SegmentKey"`
	DepartureAirport string `xml:"Departure>AirportCode"`
	DepartureDate    string `xml:"Departure>Date"`
	DepartureTime    string `xml:"Departure>Time"`
	ArrivalAirport   string `xml:"Arrival>AirportCode"`
	ArrivalDate      string `xml:"Arrival>Date"`
	ArrivalTime      string `xml:"Arrival>Time"`
	MarketingCarrier string `xml:"MarketingCarrier>AirlineID"`
	MarketingNumber  string `xml:"MarketingCarrier>FlightNumber"`
	OperatingCarrier string `xml:"OperatingCarrier>AirlineID"`
	ClassOfService   string `xml:"ClassOfService>Code"`
}

// OwnedID is an identifier carrying the code of whoever issued it. NDC uses
// this shape wherever an identifier crosses a party boundary, because the same
// string means different things to different owners.
//
// It is a struct rather than a chained field because encoding/xml cannot attach
// an attribute to a nested element path.
type OwnedID struct {
	Owner string `xml:"Owner,attr"`
	Value string `xml:",chardata"`
}

// OfferItem is one purchasable item. Only the flight detail is read: the price
// is carried through untouched because this node does not price anything.
type OfferItem struct {
	ID      OwnedID  `xml:"OfferItemID"`
	Flights []Flight `xml:"OfferItemType>DetailedFlightItem>OriginDestination>Flight"`
}

// OrderCreateRQ asks for an order to be created.
type OrderCreateRQ struct {
	XMLName    xml.Name    `xml:"OrderCreateRQ"`
	Party      Party       `xml:"Party"`
	Passengers []Passenger `xml:"Query>Passengers>Passenger"`
	OfferItems []OfferItem `xml:"Query>OrderItems>OfferItem"`
	// OfferRef is the offer named by a shopping response, when one is quoted.
	OfferRef      OwnedID `xml:"Query>OrderItems>ShoppingResponse>Offers>Offer>OfferID"`
	OfferRefOwner string  `xml:"Query>OrderItems>ShoppingResponse>Owner"`
}

// OrderRetrieveRQ asks for an existing order.
type OrderRetrieveRQ struct {
	XMLName xml.Name `xml:"OrderRetrieveRQ"`
	Party   Party    `xml:"Party"`
	Order   OwnedID  `xml:"Query>Filters>OrderID"`
}

// OrderID returns the requested order identifier.
func (m *OrderRetrieveRQ) OrderID() string { return strings.TrimSpace(m.Order.Value) }

// OrderCancelRQ asks for an order to be cancelled.
type OrderCancelRQ struct {
	XMLName xml.Name `xml:"OrderCancelRQ"`
	Party   Party    `xml:"Party"`
	Order   OwnedID  `xml:"Query>OrderID"`
}

// OrderID returns the order identifier to cancel.
func (m *OrderCancelRQ) OrderID() string { return strings.TrimSpace(m.Order.Value) }

// cardNumberRe finds a bare payment card number. Deliberately broad: this is a
// refusal gate, and a false positive costs a rejected request while a false
// negative writes a card number to disk.
var cardNumberRe = regexp.MustCompile(`(?s)<[^>]*CardNumber[^>]*>\s*([0-9 -]{12,25})\s*<`)

// CarriesCardData reports whether a payload contains payment card details.
func CarriesCardData(raw []byte) bool {
	return cardNumberRe.Match(raw)
}

// soapBody strips a SOAP envelope when one is present.
//
// NDC is defined over both plain XML and SOAP, and carriers differ, so the
// wrapper is removed rather than made the caller's problem.
func soapBody(raw []byte) []byte {
	s := string(raw)
	lower := strings.ToLower(s)
	i := strings.Index(lower, ":body>")
	if i < 0 {
		if i = strings.Index(lower, "<body>"); i < 0 {
			return raw
		}
		i += len("<body>") - 1
	}
	start := i + len(":body>")
	j := strings.LastIndex(strings.ToLower(s), ":body>")
	if j <= start {
		return raw
	}
	k := strings.LastIndex(s[:j], "</")
	if k < start {
		return raw
	}
	return []byte(s[start:k])
}

// MessageType reports which order message a payload carries.
func MessageType(raw []byte) string {
	body := soapBody(raw)
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			switch se.Name.Local {
			case MsgOrderCreateRQ, MsgOrderRetrieveRQ, MsgOrderCancelRQ, MsgOrderViewRS:
				return se.Name.Local
			}
		}
	}
}

// IsNDC reports whether a payload is one of the order messages.
func IsNDC(raw []byte) bool { return MessageType(raw) != "" }

func unmarshal(raw []byte, into any) error {
	if CarriesCardData(raw) {
		return ErrCardData
	}
	if err := xml.Unmarshal(soapBody(raw), into); err != nil {
		return fmt.Errorf("ndc: decode: %w", err)
	}
	return nil
}

// ParseOrderCreate decodes an OrderCreateRQ.
func ParseOrderCreate(raw []byte) (*OrderCreateRQ, error) {
	var m OrderCreateRQ
	if err := unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ParseOrderRetrieve decodes an OrderRetrieveRQ.
func ParseOrderRetrieve(raw []byte) (*OrderRetrieveRQ, error) {
	var m OrderRetrieveRQ
	if err := unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ParseOrderCancel decodes an OrderCancelRQ.
func ParseOrderCancel(raw []byte) (*OrderCancelRQ, error) {
	var m OrderCancelRQ
	if err := unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ToRecord maps an order request onto a canonical record.
//
// Segments come from the inline flight detail. An order that names only an
// offer cannot be resolved here, because resolving it would mean holding
// offers, which means pricing.
func (m *OrderCreateRQ) ToRecord(receivedAt time.Time) (*pnr.PNR, error) {
	if len(m.Passengers) == 0 {
		return nil, fmt.Errorf("ndc: order names no passengers")
	}
	rec := &pnr.PNR{Status: pnr.StatusOpen, CreatedAt: receivedAt, UpdatedAt: receivedAt}

	for i, p := range m.Passengers {
		pax := pnr.Passenger{
			Ref:     i + 1,
			Surname: strings.ToUpper(p.Name.Surname),
			Given:   strings.ToUpper(p.Name.Given),
			Title:   strings.ToUpper(p.Name.Title),
			Type:    paxType(p.PTC),
		}
		pax.Infant = pax.Type == pnr.PaxInfant
		rec.Passengers = append(rec.Passengers, pax)
		if p.Email != "" {
			rec.Contacts = append(rec.Contacts, pnr.Contact{Type: "email", Text: p.Email})
		}
		if p.Phone != "" {
			rec.Contacts = append(rec.Contacts, pnr.Contact{Type: "phone", Text: p.Phone})
		}
	}

	seats := len(m.Passengers)
	for _, item := range m.OfferItems {
		for _, f := range item.Flights {
			depart, err := time.Parse("2006-01-02", f.DepartureDate)
			if err != nil {
				return nil, fmt.Errorf("ndc: flight %s%s has an unreadable departure date %q",
					f.MarketingCarrier, f.MarketingNumber, f.DepartureDate)
			}
			rec.Segments = append(rec.Segments, pnr.Segment{
				Type:             pnr.SegmentAir,
				Carrier:          strings.ToUpper(f.MarketingCarrier),
				OperatingCarrier: strings.ToUpper(f.OperatingCarrier),
				FlightNum:        f.MarketingNumber,
				Class:            strings.ToUpper(f.ClassOfService),
				Depart:           depart,
				WireDate:         pnr.FormatDate(depart),
				DepartTime:       compactTime(f.DepartureTime),
				ArriveTime:       compactTime(f.ArrivalTime),
				Board:            strings.ToUpper(f.DepartureAirport),
				Off:              strings.ToUpper(f.ArrivalAirport),
				// NN: the order asks for these seats. Whether they are held is
				// the carrier's answer, not the requester's assertion.
				Status: "NN",
				Seats:  seats,
			})
		}
	}
	if len(rec.Segments) == 0 {
		return nil, ErrOfferOnly
	}
	rec.Recompute()
	rec.Origin = pnr.Origin{Party: m.Party.ID(), Channel: "ndc"}
	return rec, nil
}

func compactTime(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), ":", "")
}

// paxType maps an NDC passenger type code onto the canonical vocabulary.
func paxType(ptc string) pnr.PassengerType {
	switch strings.ToUpper(ptc) {
	case "CHD", "CNN":
		return pnr.PaxChild
	case "INF", "INS":
		return pnr.PaxInfant
	case "GRP":
		return pnr.PaxGroup
	default:
		return pnr.PaxAdult
	}
}
