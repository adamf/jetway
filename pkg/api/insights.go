package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
	"github.com/adamf/jetway/pkg/store"
)

// insights answers the questions an operator and a commercial team ask about
// the same traffic.
//
// It is computed rather than counted: the aggregate is derived from the message
// log and the record store on each request. That is honest at demo volume and
// wrong at scale -- a real deployment reads these off the metrics endpoint or
// out of the traces, where they are counted as they happen. Doing it this way
// keeps the console truthful about a store that can be wiped between requests.
type insights struct {
	Records   recordInsights   `json:"records"`
	Selling   sellingInsights  `json:"selling"`
	Documents documentInsights `json:"documents"`
	Traffic   trafficInsights  `json:"traffic"`
	Carriers  []carrierRow     `json:"carriers"`
	Queues    map[string]int   `json:"queues"`
}

type recordInsights struct {
	Total      int `json:"total"`
	Interline  int `json:"interline"`
	Ticketed   int `json:"ticketed"`
	Cancelled  int `json:"cancelled"`
	Split      int `json:"split"`
	Passengers int `json:"passengers"`
}

// sellingInsights is the commercial shape of what has been sold: how many
// seats, and what the carriers said about them.
type sellingInsights struct {
	Seats      int `json:"seats"`
	Segments   int `json:"segments"`
	Confirmed  int `json:"confirmed"`
	Waitlisted int `json:"waitlisted"`
	Refused    int `json:"refused"`
	Pending    int `json:"pending"`
	// FreeSale is segments held without a round trip, because the carrier had
	// already broadcast the class as sellable.
	FreeSale int `json:"free_sale"`
}

type documentInsights struct {
	Tickets int `json:"tickets"`
	Coupons int `json:"coupons"`
	EMDs    int `json:"emds"`
	// Ancillary is issued EMD value by reason-for-issuance, which is the
	// revenue category. Amounts are carried as written, not summed across
	// currencies, because adding GBP to USD would be a lie.
	Ancillary map[string]map[string]string `json:"ancillary"`
}

type trafficInsights struct {
	Messages    int            `json:"messages"`
	Inbound     int            `json:"inbound"`
	Outbound    int            `json:"outbound"`
	ByFormat    map[string]int `json:"by_format"`
	Duplicates  int            `json:"duplicates"`
	DeadLetter  int            `json:"dead_letter"`
	Refused     int            `json:"refused"`
	Diagnostics int            `json:"diagnostics"`
}

// carrierRow is how a partner is behaving: volume, how often they refuse, and
// how long they take. All three are commercial facts as much as operational
// ones.
type carrierRow struct {
	Carrier    string `json:"carrier"`
	Messages   int    `json:"messages"`
	Segments   int    `json:"segments"`
	Confirmed  int    `json:"confirmed"`
	Refused    int    `json:"refused"`
	Waitlisted int    `json:"waitlisted"`
	// ReplyMillis is the median time from a request going out to the answer
	// arriving, over the sampled window.
	ReplyMillis int64 `json:"reply_ms"`
	Unreachable int   `json:"unreachable"`
}

// insightsTTL is how long a computed snapshot serves repeat requests. The
// aggregate walks thousands of records and messages; twenty open consoles
// polling every couple of seconds must cost one computation, not twenty.
const insightsTTL = 2 * time.Second

func (s *Server) insights(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	limit := intParam(r, "limit", 2000)
	s.insightsMu.Lock()
	if s.insightsBody != nil && s.insightsFor == limit && time.Since(s.insightsAt) < insightsTTL {
		body := s.insightsBody
		s.insightsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
		return
	}
	s.insightsMu.Unlock()
	recs, err := s.Store.ListPNRs(ctx, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	msgs, err := s.Store.ListMessages(ctx, store.MessageFilter{Limit: limit})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	counts, err := s.Store.QueueCounts(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	out := insights{
		Queues:    counts,
		Documents: documentInsights{Ancillary: map[string]map[string]string{}},
		Traffic:   trafficInsights{ByFormat: map[string]int{}},
	}
	byCarrier := map[string]*carrierRow{}
	carrier := func(c string) *carrierRow {
		if c == "" {
			c = "(none)"
		}
		if byCarrier[c] == nil {
			byCarrier[c] = &carrierRow{Carrier: c}
		}
		return byCarrier[c]
	}

	for _, rec := range recs {
		out.Records.Total++
		out.Records.Passengers += len(rec.Passengers)
		if len(rec.Carriers()) > 1 {
			out.Records.Interline++
		}
		if rec.Ticketed() {
			out.Records.Ticketed++
		}
		if rec.Status == pnr.StatusCancelled {
			out.Records.Cancelled++
		}
		if rec.SplitFrom != "" || len(rec.SplitTo) > 0 {
			out.Records.Split++
		}
		for _, sg := range rec.Segments {
			if sg.Type != pnr.SegmentAir {
				continue
			}
			out.Selling.Segments++
			out.Selling.Seats += sg.Seats
			cr := carrier(sg.Carrier)
			cr.Segments++
			info, known := rescode.ActionCode(sg.Status).Info()
			switch {
			case known && info.Confirmed:
				out.Selling.Confirmed++
				cr.Confirmed++
			case known && info.Waitlisted:
				out.Selling.Waitlisted++
				cr.Waitlisted++
			case known && info.Category == rescode.CatReply:
				out.Selling.Refused++
				cr.Refused++
			default:
				out.Selling.Pending++
			}
		}
		for _, t := range rec.Tickets {
			if t.Type.IsEMD() {
				out.Documents.EMDs++
				for _, c := range t.Coupons {
					if c.Amount == "" {
						continue
					}
					addAmount(out.Documents.Ancillary, string(t.RFIC), c.Currency, c.Amount)
				}
				continue
			}
			out.Documents.Tickets++
			out.Documents.Coupons += len(t.Coupons)
		}
	}

	// Request-to-reply latency, matched through the correlation the pipeline
	// already records.
	sent := map[string]*store.Message{}
	latency := map[string][]int64{}
	for _, m := range msgs {
		out.Traffic.Messages++
		out.Traffic.Diagnostics += len(m.Diagnostics)
		out.Traffic.ByFormat[string(m.Format)]++
		if m.Direction == store.Inbound {
			out.Traffic.Inbound++
		} else {
			out.Traffic.Outbound++
			sent[m.ID] = m
		}
		switch m.Status {
		case store.StatusDLQ:
			out.Traffic.DeadLetter++
		case store.StatusRejected:
			out.Traffic.Refused++
		case store.StatusUndeliverable:
			carrier(peerCarrier(s, m.Peer)).Unreachable++
		}
		if strings.Contains(m.Error, "duplicate") || strings.Contains(m.Error, "retransmission") {
			out.Traffic.Duplicates++
		}
		carrier(peerCarrier(s, m.Peer)).Messages++

		if m.Direction == store.Inbound && m.CorrelationID != "" {
			if req, ok := sent[m.CorrelationID]; ok {
				if d := m.At.Sub(req.At); d > 0 {
					c := peerCarrier(s, m.Peer)
					latency[c] = append(latency[c], d.Milliseconds())
				}
			}
		}
	}
	for c, ms := range latency {
		sort.Slice(ms, func(i, j int) bool { return ms[i] < ms[j] })
		carrier(c).ReplyMillis = ms[len(ms)/2]
	}

	for _, rec := range recs {
		for _, sg := range rec.Segments {
			if sg.Type == pnr.SegmentAir && sg.Status == "HK" {
				// Held with no request having gone out is free sale; the
				// request path leaves HN behind first.
				out.Selling.FreeSale++
				break
			}
		}
	}

	rows := make([]carrierRow, 0, len(byCarrier))
	for _, r := range byCarrier {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Carrier < rows[j].Carrier })
	out.Carriers = rows

	body, err := json.Marshal(out)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.insightsMu.Lock()
	s.insightsBody, s.insightsFor, s.insightsAt = body, limit, time.Now()
	s.insightsMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(body) //nolint:errcheck
}

// addAmount accumulates a money amount per category and currency. Currencies
// are kept apart because adding them together would be a lie.
func addAmount(into map[string]map[string]string, category, currency, amount string) {
	if category == "" {
		category = "(none)"
	}
	if currency == "" {
		currency = "(none)"
	}
	if into[category] == nil {
		into[category] = map[string]string{}
	}
	prev, _ := strconv.ParseFloat(into[category][currency], 64)
	add, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return
	}
	into[category][currency] = strconv.FormatFloat(prev+add, 'f', 2, 64)
}

// peerCarrier maps a link name to the carrier behind it.
func peerCarrier(s *Server, peer string) string {
	if p := s.Gateway.Peer(peer); p != nil && p.Carrier != "" {
		return p.Carrier
	}
	return peer
}
