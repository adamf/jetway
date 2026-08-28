// Package rescode holds the IATA reservation action and status code
// vocabulary.
//
// The same two-letter codes appear in teletype AIRIMP segment elements and in
// EDIFACT RPI segments. They live in their own package because a code table
// duplicated across two decoders is a table that drifts, and a decoder that
// disagrees with its sibling about whether US means "waitlisted" produces
// bookings that quietly disagree with the partner holding the other copy.
package rescode

import "strings"

// ActionCode is a two-letter reservation action or status code. The codes below
// are the interline vocabulary; carriers also use private codes bilaterally,
// which parse fine and simply return an unknown Meaning.
type ActionCode string

// Category classifies what a code does, which is what response logic branches on.
type Category int

const (
	// CatUnknown covers private or unrecognised codes.
	CatUnknown Category = iota
	// CatRequest asks the receiving carrier to do something and expects a reply.
	CatRequest
	// CatReply answers a request.
	CatReply
	// CatHolding states the sender's current holding without asking anything.
	CatHolding
	// CatCancel withdraws a previously requested or held segment.
	CatCancel
	// CatAdvice notifies of a change with no reply expected.
	CatAdvice
)

func (c Category) String() string {
	switch c {
	case CatRequest:
		return "request"
	case CatReply:
		return "reply"
	case CatHolding:
		return "holding"
	case CatCancel:
		return "cancel"
	case CatAdvice:
		return "advice"
	}
	return "unknown"
}

// CodeInfo describes an action code.
type CodeInfo struct {
	Code     ActionCode
	Meaning  string
	Category Category
	// Confirmed reports whether the code represents a held, confirmed seat.
	Confirmed bool
	// Waitlisted reports whether the code represents a waitlist holding.
	Waitlisted bool
}

// Codes is the interline action and status code vocabulary.
var Codes = map[ActionCode]CodeInfo{
	// Requests.
	"NN": {"NN", "need, sell and report", CatRequest, false, false},
	"LL": {"LL", "add to waitlist", CatRequest, false, false},
	"SS": {"SS", "sold, segment sold from availability", CatRequest, true, false},
	"DS": {"DS", "need, do not sell if unable", CatRequest, false, false},
	"GN": {"GN", "group request, name list to follow", CatRequest, false, false},
	"PE": {"PE", "priority waitlist request", CatRequest, false, false},
	"RQ": {"RQ", "requested", CatRequest, false, false},

	// Replies.
	"KK": {"KK", "confirming", CatReply, true, false},
	"KL": {"KL", "confirming from waitlist", CatReply, true, false},
	"UC": {"UC", "unable, flight closed, waitlist closed", CatReply, false, false},
	"UN": {"UN", "unable, flight does not operate", CatReply, false, false},
	"US": {"US", "unable, have waitlisted", CatReply, false, true},
	"UU": {"UU", "unable, have waitlisted", CatReply, false, true},
	"NO": {"NO", "no action taken", CatReply, false, false},
	"TK": {"TK", "confirming, advise times changed", CatReply, true, false},
	"TL": {"TL", "waitlisted, advise times changed", CatReply, false, true},

	// Holdings.
	"HK": {"HK", "holding confirmed", CatHolding, true, false},
	"HL": {"HL", "holding waitlisted", CatHolding, false, true},
	"HN": {"HN", "holding need, requested", CatHolding, false, false},
	"RR": {"RR", "reconfirmed", CatHolding, true, false},
	"PN": {"PN", "pending, request not yet processed", CatHolding, false, false},

	// Cancellations.
	"XX": {"XX", "cancel", CatCancel, false, false},
	"XK": {"XK", "cancel, no action required", CatCancel, false, false},
	"HX": {"HX", "have cancelled", CatCancel, false, false},

	// Advice, chiefly schedule change.
	"SC": {"SC", "schedule change", CatAdvice, false, false},
	"WK": {"WK", "was confirmed, schedule change", CatAdvice, false, false},
	"WL": {"WL", "was waitlisted, schedule change", CatAdvice, false, false},
	"WN": {"WN", "was need, schedule change", CatAdvice, false, false},
	"IX": {"IX", "cancelled, if holding", CatAdvice, false, false},
	"DL": {"DL", "received deletion", CatAdvice, false, false},
	"MM": {"MM", "meet and assist", CatAdvice, false, false},
}

// Info returns the code description, and whether the code is known.
func (a ActionCode) Info() (CodeInfo, bool) {
	c, ok := Codes[ActionCode(strings.ToUpper(string(a)))]
	return c, ok
}

// Category returns the code's category, CatUnknown for private codes.
func (a ActionCode) Category() Category {
	if c, ok := a.Info(); ok {
		return c.Category
	}
	return CatUnknown
}

// Confirmed reports whether the code represents a held, confirmed seat.
func (a ActionCode) Confirmed() bool {
	c, _ := a.Info()
	return c.Confirmed
}

// Waitlisted reports whether the code represents a waitlist holding.
func (a ActionCode) Waitlisted() bool {
	c, _ := a.Info()
	return c.Waitlisted
}

// NeedsReply reports whether receiving this code obliges the receiver to answer.
func (a ActionCode) NeedsReply() bool { return a.Category() == CatRequest }

// ReplyTo returns the holding status a requester should record after receiving
// reply. It reports ok=false when reply is not a reply code.
func ReplyTo(reply ActionCode) (holding ActionCode, ok bool) {
	switch strings.ToUpper(string(reply)) {
	case "KK", "KL", "TK":
		return "HK", true
	case "US", "UU", "TL":
		return "HL", true
	case "UC", "UN", "NO":
		return "", true // nothing is held; the segment is dropped
	}
	return "", false
}
