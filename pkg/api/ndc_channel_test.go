package api

import (
	"testing"

	"github.com/adamf/jetway/pkg/ndc"
)

// An order that arrives over NDC is a sale in the NDC channel. Without
// the channel on the request a fare filed for NDC sellers alone could not
// be priced for the one kind of sale it was filed for.
func TestOrderIsASaleInTheNDCChannel(t *testing.T) {
	m, err := ndc.ParseOrderCreate([]byte(`<OrderCreateRQ xmlns="http://www.iata.org/IATA/EDIST">
  <Party><Sender><TravelAgencySender><Name>TEST AGENT</Name><AgencyID>LON1A2B</AgencyID></TravelAgencySender></Sender></Party>
  <Query>
    <Passengers><Passenger ObjectKey="T1"><PTC>ADT</PTC><Name><Surname>SELLER</Surname><Given>ANN</Given></Name></Passenger></Passengers>
    <OrderItems><OfferItem>
      <OfferItemID Owner="BA">1</OfferItemID>
      <OfferItemType><DetailedFlightItem refs="T1"><OriginDestination><Flight>
        <SegmentKey>BA2764</SegmentKey>
        <Departure><AirportCode>LGW</AirportCode><Date>2026-12-20</Date><Time>09:15</Time></Departure>
        <Arrival><AirportCode>AMS</AirportCode></Arrival>
        <MarketingCarrier><AirlineID>BA</AirlineID><FlightNumber>2764</FlightNumber></MarketingCarrier>
        <ClassOfService><Code>L</Code></ClassOfService>
      </Flight></OriginDestination></DetailedFlightItem></OfferItemType>
    </OfferItem></OrderItems>
  </Query>
</OrderCreateRQ>`))
	if err != nil {
		t.Fatal(err)
	}
	req, err := bookingFromOrder(m)
	if err != nil {
		t.Fatal(err)
	}
	if req.Channel != "ndc" || req.Agent != "LON1A2B" || len(req.Segments) != 1 {
		t.Errorf("channel %q, agent %q, %d segments", req.Channel, req.Agent, len(req.Segments))
	}
}
