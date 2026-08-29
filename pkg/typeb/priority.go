package typeb

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

// PriorityClass groups priority codes into the bands a store-and-forward
// network actually services differently.
//
// Bands rather than a total order, deliberately. The published material names
// the codes and what they mean but does not settle a precise ranking between,
// say, QX and QK, and inventing one would be a guess dressed as a rule. Three
// bands are enough to do the thing that matters: not making an urgent message
// wait behind a bulk one.
type PriorityClass int

const (
	// PriorityUrgent is serviced first: network service messages and traffic
	// the sender marked urgent.
	PriorityUrgent PriorityClass = iota
	// PriorityNormal is everything else, including codes this package does not
	// recognise. An unknown code is never starved.
	PriorityNormal
	// PriorityDeferred is serviced last: traffic the sender said may wait.
	PriorityDeferred
)

func (c PriorityClass) String() string {
	switch c {
	case PriorityUrgent:
		return "urgent"
	case PriorityDeferred:
		return "deferred"
	}
	return "normal"
}

// priorityClasses maps the codes whose band is unambiguous. Anything absent is
// normal, which is the safe default: it delays nothing and starves nothing.
var priorityClasses = map[string]PriorityClass{
	"QS": PriorityUrgent, // service
	"QU": PriorityUrgent, // urgent
	"QP": PriorityUrgent, // priority
	"QD": PriorityDeferred,
	"QN": PriorityDeferred, // bulk
}

// ClassOf returns the service band for a priority code.
//
// The miss is handled explicitly rather than left to the zero value. Urgent
// sorts first, so it has to be the zero of the type for ordering to work, which
// would otherwise make every unrecognised code urgent -- the opposite of the
// documented rule and a way for one unknown code to jump every real queue.
func ClassOf(code string) PriorityClass {
	if c, ok := priorityClasses[strings.ToUpper(strings.TrimSpace(code))]; ok {
		return c
	}
	return PriorityNormal
}

var priorityLineRe = regexp.MustCompile(`^\s*(Q[A-Z])(?:\s|$)`)

// PriorityOf reads the priority code from encoded Type B without a full parse.
//
// It exists so the egress side can order a queue by priority without decoding
// what it is about to resend: the bytes being retransmitted are the captured
// bytes, and nothing on that path should depend on re-reading them as
// structure.
func PriorityOf(raw []byte) string {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		t := bytes.TrimSpace(line)
		if len(t) == 0 {
			continue
		}
		// Skip network framing, which precedes the priority line.
		if bytes.HasPrefix(t, []byte(StartOfMessage)) {
			continue
		}
		if m := priorityLineRe.FindSubmatch(t); m != nil {
			return string(m[1])
		}
		return ""
	}
	return ""
}

var channelRe = regexp.MustCompile(`^([A-Z]*)(\d+)$`)

// ParseChannel splits a channel token such as "ABC1234" into its channel
// identifier and sequence number.
//
// The token follows ZCZC and is how a store-and-forward link numbers what it
// sends. Reading it is what makes a missing message detectable at all: without
// the sequence, a message that never arrived is indistinguishable from a
// message that was never sent.
func ParseChannel(token string) (channel string, seq int, ok bool) {
	m := channelRe.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(token)))
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

// SequenceGap describes what a sequence number said about the messages before
// it.
type SequenceGap struct {
	// Expected is the number that should have come next.
	Expected int
	// Got is the number that arrived.
	Got int
	// Missing is how many messages the jump skipped, zero when none were.
	Missing int
	// Repeat reports that the number went backwards or repeated, which means a
	// retransmission or a sender that restarted its counter.
	Repeat bool
}

// CheckSequence compares an arriving sequence number against the last one seen
// on the same channel.
//
// Wrap is the number the counter returns to after its highest value. Passing
// zero disables wrap handling, which will report a very large gap when a
// counter rolls over; a link whose width is known should say so.
func CheckSequence(last, got, wrap int) (SequenceGap, bool) {
	expected := last + 1
	if wrap > 0 && expected > wrap {
		expected = 1
	}
	if got == expected {
		return SequenceGap{}, false
	}
	g := SequenceGap{Expected: expected, Got: got}
	if wrap > 0 {
		// With a counter that wraps, "ahead" and "behind" are the same
		// arithmetic and can only be told apart by convention: a short forward
		// distance is a hole, a long one is the number having gone backwards.
		d := got - expected
		if d < 0 {
			d += wrap
		}
		if d < wrap/2 {
			g.Missing = d
		} else {
			g.Repeat = true
		}
		return g, true
	}
	if got < expected {
		g.Repeat = true
	} else {
		g.Missing = got - expected
	}
	return g, true
}
