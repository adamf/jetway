package ndc

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
)

// viewFlight is a flight as it appears in a response.
type viewFlight struct {
	SegmentKey       string     `xml:"SegmentKey"`
	DepartureAirport string     `xml:"Departure>AirportCode"`
	DepartureDate    string     `xml:"Departure>Date"`
	DepartureTime    string     `xml:"Departure>Time,omitempty"`
	ArrivalAirport   string     `xml:"Arrival>AirportCode"`
	ArrivalTime      string     `xml:"Arrival>Time,omitempty"`
	MarketingCarrier string     `xml:"MarketingCarrier>AirlineID"`
	MarketingNumber  string     `xml:"MarketingCarrier>FlightNumber"`
	OperatingCarrier *carrierID `xml:"OperatingCarrier,omitempty"`
	ClassCode        string     `xml:"ClassOfService>Code"`
	// StatusCode carries the reservation status. NDC has no field that means
	// the same thing as an IATA status code, so it travels here with its
	// meaning spelled out rather than being silently dropped.
	StatusCode string `xml:"ClassOfService>MarketingName,omitempty"`
}

// carrierID is a pointer-shaped wrapper so the whole element disappears when
// there is no operating carrier, rather than rendering as an empty tag a
// schema-validating client will reject.
type carrierID struct {
	AirlineID string `xml:"AirlineID"`
}

type bookingReference struct {
	ID        string `xml:"ID"`
	AirlineID string `xml:"AirlineID,omitempty"`
}

type orderItem struct {
	ID      OwnedID      `xml:"OrderItemID"`
	Flights []viewFlight `xml:"FlightItem>OriginDestination>Flight"`
}

type viewPassenger struct {
	ObjectKey string  `xml:"ObjectKey,attr"`
	PTC       string  `xml:"PTC"`
	Name      PaxName `xml:"Name"`
}

type order struct {
	ID                OwnedID            `xml:"OrderID"`
	BookingReferences []bookingReference `xml:"BookingReferences>BookingReference,omitempty"`
	Items             []orderItem        `xml:"OrderItems>OrderItem"`
	TimeLimit         string             `xml:"TimeLimits>PaymentTimeLimit>DateTime,omitempty"`
}

// Error is one problem reported in a response.
type Error struct {
	Type      string `xml:"Type,attr,omitempty"`
	ShortText string `xml:"ShortText,attr,omitempty"`
	Detail    string `xml:",chardata"`
}

type errorsBlock struct {
	Error []Error `xml:"Error"`
}

type responseBlock struct {
	Order *order          `xml:"Order,omitempty"`
	Pax   []viewPassenger `xml:"DataLists>PassengerList>Passenger,omitempty"`
}

// OrderViewRS is the response carrying an order.
//
// Success, Errors and Response are pointers so that the elements are absent
// rather than empty. An empty <Errors/> alongside a <Success/> says two
// contradictory things, and NDC clients validate.
type OrderViewRS struct {
	XMLName  xml.Name       `xml:"OrderViewRS"`
	NS       string         `xml:"xmlns,attr"`
	Success  *struct{}      `xml:"Success,omitempty"`
	Errors   *errorsBlock   `xml:"Errors,omitempty"`
	Response *responseBlock `xml:"Response,omitempty"`
}

// BuildOrderView renders a record as an OrderViewRS.
//
// The order identifier is the record locator. That is the honest mapping: it is
// the reference both sides already use, and minting a second identifier would
// mean keeping the two in step forever.
func BuildOrderView(rec *pnr.PNR, owner string) ([]byte, error) {
	if rec == nil {
		return nil, fmt.Errorf("ndc: no record to render")
	}
	o := &order{ID: OwnedID{Owner: owner, Value: rec.RecordLocator}}

	// The carrier's own references matter more to the requester than ours: they
	// are what the carrier will recognise if anybody rings them up.
	o.BookingReferences = append(o.BookingReferences, bookingReference{ID: rec.RecordLocator, AirlineID: owner})
	for _, l := range rec.Locators {
		if l.Value != "" && l.Value != rec.RecordLocator {
			o.BookingReferences = append(o.BookingReferences, bookingReference{ID: l.Value, AirlineID: l.Owner})
		}
	}

	for i := range rec.Segments {
		s := &rec.Segments[i]
		if s.Type != pnr.SegmentAir {
			continue
		}
		f := viewFlight{
			SegmentKey:       s.Carrier + s.FlightNum,
			DepartureAirport: s.Board,
			DepartureDate:    s.Depart.Format("2006-01-02"),
			DepartureTime:    colonTime(s.DepartTime),
			ArrivalAirport:   s.Off,
			ArrivalTime:      colonTime(s.ArriveTime),
			MarketingCarrier: s.Carrier,
			MarketingNumber:  strings.TrimLeft(s.FlightNum, "0"),
			ClassCode:        s.Class,
			StatusCode:       statusText(s.Status),
		}
		if s.OperatingCarrier != "" && s.OperatingCarrier != s.Carrier {
			f.OperatingCarrier = &carrierID{AirlineID: s.OperatingCarrier}
		}
		o.Items = append(o.Items, orderItem{
			ID:      OwnedID{Owner: owner, Value: strconv.Itoa(s.Ref)},
			Flights: []viewFlight{f},
		})
	}

	for _, t := range rec.Ticketing {
		if t.Deadline != nil {
			o.TimeLimit = t.Deadline.UTC().Format(time.RFC3339)
			break
		}
	}

	rs := &OrderViewRS{NS: Namespace, Success: &struct{}{}, Response: &responseBlock{Order: o}}
	for _, p := range rec.Passengers {
		rs.Response.Pax = append(rs.Response.Pax, viewPassenger{
			ObjectKey: "T" + strconv.Itoa(p.Ref),
			PTC:       ptcFor(p),
			Name:      PaxName{Surname: p.Surname, Given: p.Given, Title: p.Title},
		})
	}
	return marshal(rs)
}

// BuildError renders a refusal.
//
// It is an OrderViewRS carrying Errors rather than a separate message type,
// because that is where an NDC client looks for them.
func BuildError(errType, shortText, detail string) ([]byte, error) {
	return marshal(&OrderViewRS{
		NS:     Namespace,
		Errors: &errorsBlock{Error: []Error{{Type: errType, ShortText: shortText, Detail: detail}}},
	})
}

func marshal(v any) ([]byte, error) {
	body, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("ndc: encode: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
}

// statusText spells out a reservation status, since NDC carries no equivalent
// field and a bare two-letter code would mean nothing to a client that has
// never seen AIRIMP.
func statusText(status string) string {
	if status == "" {
		return ""
	}
	if info, ok := rescode.ActionCode(status).Info(); ok {
		return status + " " + info.Meaning
	}
	return status
}

func colonTime(hhmm string) string {
	if len(hhmm) != 4 {
		return ""
	}
	return hhmm[:2] + ":" + hhmm[2:]
}

func ptcFor(p pnr.Passenger) string {
	switch p.Type {
	case pnr.PaxChild:
		return "CHD"
	case pnr.PaxInfant:
		return "INF"
	case pnr.PaxGroup:
		return "GRP"
	default:
		return "ADT"
	}
}

// BuildPartialCancel renders an order that was cancelled here but which one or
// more carriers could not be told about.
//
// It carries both the order and an error, which looks contradictory and is
// exactly the situation: the order is cancelled on this side, and a carrier
// that was not reached is still holding seats against it. Reporting only the
// success would tell the requester their seats are released when they may not
// be.
func BuildPartialCancel(rec *pnr.PNR, owner string, unreachable []string) ([]byte, error) {
	body, err := BuildOrderView(rec, owner)
	if err != nil {
		return nil, err
	}
	var rs OrderViewRS
	if err := xml.Unmarshal(body, &rs); err != nil {
		return nil, fmt.Errorf("ndc: encode partial cancellation: %w", err)
	}
	rs.NS = Namespace
	rs.Success = nil
	rs.Errors = &errorsBlock{Error: []Error{{
		Type: "202", ShortText: "Cancelled, carriers not all notified",
		Detail: "the order is cancelled here, but " + strings.Join(unreachable, ", ") +
			" could not be told and may still hold the seats",
	}}}
	return marshal(&rs)
}
