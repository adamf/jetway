package ndc

import (
	"testing"
	"time"
)

func FuzzParseOrders(f *testing.F) {
	f.Add([]byte(orderCreate))
	f.Add([]byte(`<OrderRetrieveRQ xmlns="http://www.iata.org/IATA/EDIST/2017.2"><Query><Filters><OrderID Owner="BA">ABC123</OrderID></Filters></Query></OrderRetrieveRQ>`))
	f.Add([]byte(`<OrderCancelRQ xmlns="http://www.iata.org/IATA/EDIST/2017.2"><Query><OrderID Owner="BA">ABC123</OrderID></Query></OrderCancelRQ>`))
	f.Add([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><OrderRetrieveRQ><Query><Filters><OrderID Owner="BA">ABC123</OrderID></Filters></Query></OrderRetrieveRQ></soap:Body></soap:Envelope>`))
	f.Add([]byte(`<!DOCTYPE x [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">]><OrderCreateRQ><Query>&b;&b;&b;</Query></OrderCreateRQ>`))
	f.Add([]byte(`<OrderCreateRQ><Query><Payments><Payment><Method><PaymentCard><CardNumber>4111 1111 1111 1111</CardNumber></PaymentCard></Method></Payment></Payments></Query></OrderCreateRQ>`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_ = CarriesCardData(raw)
		_ = IsNDC(raw)
		switch MessageType(raw) {
		case MsgOrderCreateRQ:
			m, err := ParseOrderCreate(raw)
			if err != nil {
				return
			}
			_ = m.Party.ID()
			rec, err := m.ToRecord(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return
			}
			if _, err := BuildOrderView(rec, "BA"); err != nil {
				return
			}
			_, _ = BuildPartialCancel(rec, "BA", []string{"AA"})
		case MsgOrderRetrieveRQ:
			if m, err := ParseOrderRetrieve(raw); err == nil {
				_ = m.OrderID()
				_ = m.Party.ID()
			}
		case MsgOrderCancelRQ:
			if m, err := ParseOrderCancel(raw); err == nil {
				_ = m.OrderID()
				_ = m.Party.ID()
			}
		default:
			// Still run the decoders: the HTTP handler gates on MessageType,
			// but a decoder that panics on a mismatched root is a bug.
			_, _ = ParseOrderCreate(raw)
			_, _ = ParseOrderRetrieve(raw)
			_, _ = ParseOrderCancel(raw)
		}
	})
}
