package ndc

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// orderCreate is the shape a real carrier endpoint receives: SOAP-wrapped,
// EDIST namespace, flights inline in a detailed flight item.
const orderCreate = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <n:OrderCreateRQ xmlns:n="http://www.iata.org/IATA/EDIST">
      <n:Document><n:MessageVersion>1.1</n:MessageVersion></n:Document>
      <n:Party><n:Sender><n:TravelAgencySender>
        <n:Name>TEST AGENT</n:Name>
        <n:AgencyID>LON1A2B</n:AgencyID>
      </n:TravelAgencySender></n:Sender></n:Party>
      <n:Query>
        <n:Passengers>
          <n:Passenger ObjectKey="T1">
            <n:PTC>ADT</n:PTC>
            <n:Name><n:Surname>fletcher</n:Surname><n:Given>adam</n:Given><n:Title>mr</n:Title></n:Name>
            <n:Contacts><n:Contact>
              <n:EmailContact><n:Address>a@example.com</n:Address></n:EmailContact>
              <n:PhoneContact><n:Number CountryCode="44">2087123456</n:Number></n:PhoneContact>
            </n:Contact></n:Contacts>
          </n:Passenger>
          <n:Passenger ObjectKey="T2">
            <n:PTC>CHD</n:PTC>
            <n:Name><n:Surname>fletcher</n:Surname><n:Given>sam</n:Given></n:Name>
          </n:Passenger>
        </n:Passengers>
        <n:OrderItems>
          <n:OfferItem>
            <n:OfferItemID Owner="BA">1</n:OfferItemID>
            <n:OfferItemType><n:DetailedFlightItem refs="T1">
              <n:OriginDestination><n:Flight>
                <n:SegmentKey>BA2764</n:SegmentKey>
                <n:Departure><n:AirportCode>LGW</n:AirportCode><n:Date>2026-12-20</n:Date><n:Time>09:15</n:Time></n:Departure>
                <n:Arrival><n:AirportCode>AMS</n:AirportCode><n:Time>11:40</n:Time></n:Arrival>
                <n:MarketingCarrier><n:AirlineID>BA</n:AirlineID><n:FlightNumber>2764</n:FlightNumber></n:MarketingCarrier>
                <n:ClassOfService><n:Code>L</n:Code></n:ClassOfService>
              </n:Flight></n:OriginDestination>
            </n:DetailedFlightItem></n:OfferItemType>
          </n:OfferItem>
        </n:OrderItems>
      </n:Query>
    </n:OrderCreateRQ>
  </s:Body>
</s:Envelope>`

func TestMessageTypeSeesThroughSOAP(t *testing.T) {
	if got := MessageType([]byte(orderCreate)); got != MsgOrderCreateRQ {
		t.Errorf("MessageType = %q, want %q", got, MsgOrderCreateRQ)
	}
	if !IsNDC([]byte(orderCreate)) {
		t.Error("IsNDC did not recognise an order")
	}
	if IsNDC([]byte("UNB+UNOA:3+AA:ZZ'")) {
		t.Error("IsNDC matched an EDIFACT interchange")
	}
	// Plain XML, no SOAP wrapper, is equally valid.
	plain := `<OrderCancelRQ xmlns="http://www.iata.org/IATA/EDIST"><Query><OrderID Owner="BA">ABC123</OrderID></Query></OrderCancelRQ>`
	if got := MessageType([]byte(plain)); got != MsgOrderCancelRQ {
		t.Errorf("unwrapped MessageType = %q", got)
	}
}

func TestOrderCreateMapsOntoARecord(t *testing.T) {
	m, err := ParseOrderCreate([]byte(orderCreate))
	if err != nil {
		t.Fatalf("ParseOrderCreate: %v", err)
	}
	if m.Party.ID() != "LON1A2B" {
		t.Errorf("Party.ID = %q", m.Party.ID())
	}

	rec, err := m.ToRecord(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ToRecord: %v", err)
	}
	if len(rec.Passengers) != 2 {
		t.Fatalf("Passengers = %d, want 2", len(rec.Passengers))
	}
	// Names go on the wire uppercase whatever case they arrived in.
	if rec.Passengers[0].Surname != "FLETCHER" || rec.Passengers[0].Given != "ADAM" {
		t.Errorf("passenger 1 = %+v", rec.Passengers[0])
	}
	if rec.Passengers[1].Type != pnr.PaxChild {
		t.Errorf("PTC CHD must map to a child, got %q", rec.Passengers[1].Type)
	}
	if len(rec.Segments) != 1 {
		t.Fatalf("Segments = %d", len(rec.Segments))
	}
	s := rec.Segments[0]
	if s.Carrier != "BA" || s.FlightNum != "2764" || s.Board != "LGW" || s.Off != "AMS" || s.Class != "L" {
		t.Errorf("segment = %+v", s)
	}
	if s.WireDate != "20DEC" {
		t.Errorf("WireDate = %q, want 20DEC", s.WireDate)
	}
	if s.DepartTime != "0915" || s.ArriveTime != "1140" {
		t.Errorf("times = %q/%q", s.DepartTime, s.ArriveTime)
	}
	// The requester asks; the carrier answers. An order must not assert a hold.
	if s.Status != "NN" {
		t.Errorf("Status = %q, want NN", s.Status)
	}
	if s.Seats != 2 {
		t.Errorf("Seats = %d, want one per passenger", s.Seats)
	}
	if rec.Origin.Channel != "ndc" {
		t.Errorf("Origin = %+v", rec.Origin)
	}
	var email, phone bool
	for _, c := range rec.Contacts {
		email = email || c.Type == "email"
		phone = phone || c.Type == "phone"
	}
	if !email || !phone {
		t.Errorf("contacts lost: %+v", rec.Contacts)
	}
}

func TestOrderWithNoInlineFlightsIsRefused(t *testing.T) {
	offerOnly := `<OrderCreateRQ xmlns="http://www.iata.org/IATA/EDIST"><Query>
	  <Passengers><Passenger ObjectKey="T1"><PTC>ADT</PTC>
	    <Name><Surname>X</Surname><Given>Y</Given></Name></Passenger></Passengers>
	  <OrderItems><ShoppingResponse><Owner>BA</Owner>
	    <Offers><Offer><OfferID Owner="BA">OFFER1</OfferID></Offer></Offers>
	  </ShoppingResponse></OrderItems></Query></OrderCreateRQ>`
	m, err := ParseOrderCreate([]byte(offerOnly))
	if err != nil {
		t.Fatal(err)
	}
	// Resolving this would mean holding offers, which means pricing.
	if _, err := m.ToRecord(time.Now()); !errors.Is(err, ErrOfferOnly) {
		t.Errorf("ToRecord = %v, want ErrOfferOnly", err)
	}
}

func TestCardDataIsRefusedBeforeAnythingIsStored(t *testing.T) {
	withCard := strings.Replace(orderCreate, "</n:Query>",
		`<n:Payments><n:Payment><n:Method><n:PaymentCard>
		  <n:CardCode>VI</n:CardCode><n:CardNumber>4111111111111111</n:CardNumber>
		</n:PaymentCard></n:Method></n:Payment></n:Payments></n:Query>`, 1)

	if !CarriesCardData([]byte(withCard)) {
		t.Fatal("a payload with a card number was not detected")
	}
	if _, err := ParseOrderCreate([]byte(withCard)); !errors.Is(err, ErrCardData) {
		t.Errorf("ParseOrderCreate = %v, want ErrCardData", err)
	}
	// The clean payload must not trip the gate.
	if CarriesCardData([]byte(orderCreate)) {
		t.Error("a payload with no card details was rejected")
	}
}

func TestOrderRetrieveAndCancel(t *testing.T) {
	r, err := ParseOrderRetrieve([]byte(
		`<OrderRetrieveRQ xmlns="http://www.iata.org/IATA/EDIST"><Query><Filters>
		  <OrderID Owner="BA">23CRMB</OrderID></Filters></Query></OrderRetrieveRQ>`))
	if err != nil {
		t.Fatal(err)
	}
	if r.OrderID() != "23CRMB" || r.Order.Owner != "BA" {
		t.Errorf("retrieve = %+v", r)
	}

	c, err := ParseOrderCancel([]byte(
		`<OrderCancelRQ xmlns="http://www.iata.org/IATA/EDIST"><Query>
		  <OrderID Owner="BA">23CRMB</OrderID></Query></OrderCancelRQ>`))
	if err != nil {
		t.Fatal(err)
	}
	if c.OrderID() != "23CRMB" || c.Order.Owner != "BA" {
		t.Errorf("cancel = %+v", c)
	}
}

func TestBuildOrderView(t *testing.T) {
	depart := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
	deadline := time.Date(2026, 12, 1, 23, 59, 0, 0, time.UTC)
	rec := &pnr.PNR{
		RecordLocator: "ABC23D", Status: pnr.StatusOpen,
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "FLETCHER", Given: "ADAM", Title: "MR"}},
		Segments: []pnr.Segment{{
			Ref: 1, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117", Class: "Y",
			Depart: depart, DepartTime: "0915", ArriveTime: "1140",
			Board: "LHR", Off: "JFK", Status: "HK", Seats: 1,
		}},
		Locators:  []pnr.ExternalLocator{{Owner: "BA", Value: "XY7QP2"}},
		Ticketing: []pnr.Ticketing{{Text: "TKTL", Deadline: &deadline}},
	}

	out, err := BuildOrderView(rec, "1J")
	if err != nil {
		t.Fatalf("BuildOrderView: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`xmlns="http://www.iata.org/IATA/EDIST"`,
		"<OrderID Owner=\"1J\">ABC23D</OrderID>",
		"<ID>XY7QP2</ID>", // the carrier's own reference is what they will recognise
		"<AirlineID>BA</AirlineID>",
		"<Date>2026-12-20</Date>",
		"<Time>09:15</Time>",
		"<FlightNumber>117</FlightNumber>", // leading zeros are a teletype habit
		"2026-12-01T23:59:00Z",             // ticketing time limit
		"<Surname>FLETCHER</Surname>",
		"<PTC>ADT</PTC>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("OrderViewRS missing %q:\n%s", want, s)
		}
	}
	// A bare two-letter status means nothing to a client that has never seen
	// AIRIMP, so it is spelled out.
	if !strings.Contains(s, "HK holding confirmed") {
		t.Errorf("status not explained:\n%s", s)
	}
}

func TestBuildError(t *testing.T) {
	out, err := BuildError("400", "Unknown order", "no record matches order ABC23D")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `ShortText="Unknown order"`) || !strings.Contains(s, "no record matches") {
		t.Errorf("error response wrong:\n%s", s)
	}
	if strings.Contains(s, "<Success>") {
		t.Error("an error response must not also claim success")
	}
}

func TestResponsesDoNotCarryEmptyElements(t *testing.T) {
	rec := &pnr.PNR{
		RecordLocator: "EMP001", Status: pnr.StatusOpen,
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "X", Given: "Y"}},
		Segments: []pnr.Segment{{
			Ref: 1, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117",
			Depart: time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC),
			Board:  "LHR", Off: "JFK", Status: "HK",
		}},
	}
	out, err := BuildOrderView(rec, "1J")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// An empty <Errors/> beside a <Success/> says two contradictory things.
	if strings.Contains(s, "<Errors>") {
		t.Errorf("success response carries an Errors block:\n%s", s)
	}
	// A schema-validating client rejects an empty airline identifier.
	if strings.Contains(s, "<OperatingCarrier>") {
		t.Errorf("empty OperatingCarrier rendered:\n%s", s)
	}

	// An error response is the mirror image.
	e, err := BuildError("404", "Unknown order", "nope")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(e), "<Success>") || strings.Contains(string(e), "<Response>") {
		t.Errorf("error response claims success:\n%s", e)
	}
}

func TestOperatingCarrierAppearsOnlyWhenItDiffers(t *testing.T) {
	base := func(op string) string {
		rec := &pnr.PNR{
			RecordLocator: "OPC001", Status: pnr.StatusOpen,
			Segments: []pnr.Segment{{
				Ref: 1, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117",
				OperatingCarrier: op, Depart: time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC),
				Board: "LHR", Off: "JFK", Status: "HK",
			}},
		}
		out, err := BuildOrderView(rec, "1J")
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	// A codeshare is worth saying; BA operating its own flight is not.
	if !strings.Contains(base("AA"), "<AirlineID>AA</AirlineID>") {
		t.Error("a differing operating carrier must be shown")
	}
	if strings.Contains(base("BA"), "<OperatingCarrier>") {
		t.Error("an operating carrier equal to the marketing carrier is noise")
	}
}
