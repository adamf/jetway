package edifact

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Charset is an EDIFACT character repertoire named by UNB S001/0001.
type Charset struct {
	Name string
	// allowed is nil for repertoires validated by predicate rather than by set.
	allowed map[rune]bool
	pred    func(rune) bool
}

// FirstInvalid returns the first rune of s outside the repertoire.
func (c *Charset) FirstInvalid(s string) (rune, bool) {
	if c.pred != nil {
		if !utf8.ValidString(s) {
			return utf8.RuneError, false
		}
		for _, r := range s {
			if !c.pred(r) {
				return r, false
			}
		}
		return 0, true
	}
	for _, r := range s {
		if !c.allowed[r] {
			return r, false
		}
	}
	return 0, true
}

// Valid reports whether every rune of s is in the repertoire.
func (c *Charset) Valid(s string) bool {
	_, ok := c.FirstInvalid(s)
	return ok
}

func setCharset(name string, ranges [][2]rune, extra string) *Charset {
	c := &Charset{Name: name, allowed: map[rune]bool{}}
	for _, rg := range ranges {
		for r := rg[0]; r <= rg[1]; r++ {
			c.allowed[r] = true
		}
	}
	for _, r := range extra {
		c.allowed[r] = true
	}
	return c
}

// Level A punctuation from ISO 9735. This set is exact and worth trusting.
const levelAPunct = ` .,-()/='+:?!"%&*;<>`

// Level B adds lowercase letters and a handful of symbols. Implementations
// differ slightly on the tail of this list; the extras here are the ones IATA
// traffic actually uses. Widen it in configuration if a partner needs more.
const levelBExtra = "#@[]_{}\\|~"

var (
	// CharsetUNOA is ISO 9735 Level A: the safest repertoire, and what most
	// IATA PADIS traffic declares.
	CharsetUNOA = setCharset("UNOA", [][2]rune{{'A', 'Z'}, {'0', '9'}}, levelAPunct)

	// CharsetUNOB is Level B: Level A plus lowercase and common symbols.
	CharsetUNOB = setCharset("UNOB", [][2]rune{{'A', 'Z'}, {'a', 'z'}, {'0', '9'}}, levelAPunct+levelBExtra)

	// CharsetUNOC is Level C, ISO 8859-1. Validated as "printable Latin-1",
	// which is an approximation: control characters are rejected, everything
	// else in the Latin-1 range is accepted.
	CharsetUNOC = &Charset{Name: "UNOC", pred: func(r rune) bool {
		return r <= 0xFF && (r == ' ' || unicode.IsPrint(r))
	}}

	// CharsetUNOY is UTF-8 (ISO 10646-1). Rare in IATA traffic but legal.
	CharsetUNOY = &Charset{Name: "UNOY", pred: func(r rune) bool {
		return r != utf8.RuneError && (r == ' ' || unicode.IsPrint(r))
	}}
)

// CharsetByName resolves a UNB syntax identifier to a repertoire. It returns
// nil for repertoires this build does not validate, which the caller should
// treat as "skip validation", never as "reject".
func CharsetByName(name string) *Charset {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "UNOA":
		return CharsetUNOA
	case "UNOB":
		return CharsetUNOB
	// Levels C through K are the ISO 8859 parts. Validating them all as
	// printable 8-bit is deliberate: distinguishing part 2 from part 5 catches
	// nothing a gateway can act on.
	case "UNOC", "UNOD", "UNOE", "UNOF", "UNOG", "UNOH", "UNOI", "UNOJ", "UNOK":
		return CharsetUNOC
	case "UNOY":
		return CharsetUNOY
	default:
		return nil
	}
}
