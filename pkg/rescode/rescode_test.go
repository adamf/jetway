package rescode

import "testing"

func TestCategories(t *testing.T) {
	cases := []struct {
		code ActionCode
		cat  Category
	}{
		{"NN", CatRequest}, {"LL", CatRequest}, {"SS", CatRequest},
		{"KK", CatReply}, {"UC", CatReply}, {"US", CatReply},
		{"HK", CatHolding}, {"HL", CatHolding},
		{"XX", CatCancel}, {"HX", CatCancel},
		{"SC", CatAdvice}, {"WK", CatAdvice},
		{"QQ", CatUnknown}, {"", CatUnknown},
	}
	for _, c := range cases {
		if got := c.code.Category(); got != c.cat {
			t.Errorf("%q category = %v, want %v", c.code, got, c.cat)
		}
	}
}

func TestConfirmedAndWaitlisted(t *testing.T) {
	for _, c := range []ActionCode{"HK", "KK", "KL", "RR", "TK", "SS"} {
		if !c.Confirmed() {
			t.Errorf("%q should be confirmed", c)
		}
	}
	for _, c := range []ActionCode{"HL", "US", "UU", "TL"} {
		if !c.Waitlisted() {
			t.Errorf("%q should be waitlisted", c)
		}
		if c.Confirmed() {
			t.Errorf("%q must not be both confirmed and waitlisted", c)
		}
	}
	for _, c := range []ActionCode{"UC", "UN", "XX", "NN", "HN"} {
		if c.Confirmed() || c.Waitlisted() {
			t.Errorf("%q holds nothing", c)
		}
	}
}

func TestNeedsReply(t *testing.T) {
	if !ActionCode("NN").NeedsReply() {
		t.Error("NN obliges a reply")
	}
	for _, c := range []ActionCode{"KK", "HK", "XX", "SC", "ZZ"} {
		if c.NeedsReply() {
			t.Errorf("%q must not oblige a reply", c)
		}
	}
}

// ReplyTo is what moves a requester's record on when an answer arrives. Getting
// it wrong leaves a booking permanently disagreeing with the carrier's copy.
func TestReplyTo(t *testing.T) {
	cases := []struct {
		reply   ActionCode
		holding ActionCode
		isReply bool
	}{
		{"KK", "HK", true}, {"KL", "HK", true}, {"TK", "HK", true},
		{"US", "HL", true}, {"UU", "HL", true}, {"TL", "HL", true},
		{"UC", "", true}, {"UN", "", true}, {"NO", "", true},
		{"NN", "", false}, {"HK", "", false}, {"ZZ", "", false},
	}
	for _, c := range cases {
		h, ok := ReplyTo(c.reply)
		if h != c.holding || ok != c.isReply {
			t.Errorf("ReplyTo(%q) = %q,%v; want %q,%v", c.reply, h, ok, c.holding, c.isReply)
		}
	}
}

func TestLookupIsCaseInsensitive(t *testing.T) {
	if _, ok := ActionCode("kk").Info(); !ok {
		t.Error("lowercase codes should resolve")
	}
	if _, ok := ActionCode("ZZ").Info(); ok {
		t.Error("an unknown code must report as unknown")
	}
}

// Every entry must be self-consistent: the table is the shared source of truth
// for both wire formats, so an inconsistency here shows up as two decoders
// disagreeing about the same booking.
func TestTableIsSelfConsistent(t *testing.T) {
	for code, info := range Codes {
		if info.Code != code {
			t.Errorf("entry %q carries code %q", code, info.Code)
		}
		if info.Confirmed && info.Waitlisted {
			t.Errorf("%q is both confirmed and waitlisted", code)
		}
		if info.Meaning == "" {
			t.Errorf("%q has no meaning", code)
		}
		if len(code) != 2 {
			t.Errorf("%q is not a two-character code", code)
		}
		if h, isReply := ReplyTo(code); isReply != (info.Category == CatReply) {
			t.Errorf("%q: ReplyTo says isReply=%v but category is %v", code, isReply, info.Category)
		} else if isReply && h != "" {
			if got := Codes[h]; got.Confirmed != info.Confirmed || got.Waitlisted != info.Waitlisted {
				t.Errorf("%q maps to holding %q whose disposition differs", code, h)
			}
		}
	}
}
