package paxlst

import (
	"testing"
	"time"
)

// SSR DOCS as agencies and airlines publish it for entry, read into the
// document, holder and dates the passenger list needs.
func TestParseDOCS(t *testing.T) {
	d, ok := ParseDOCS("P/GBR/P123456/GBR/14MAY80/F/31JAN30/SMITH/JANE")
	if !ok {
		t.Fatal("a full DOCS must parse")
	}
	if d.Type != "P" || d.Issuer != "GBR" || d.Number != "P123456" || d.Nationality != "GBR" || d.Gender != "F" || d.Surname != "SMITH" || d.Given != "JANE" {
		t.Fatalf("fields: %+v", d)
	}
	if !d.DateOfBirth.Equal(time.Date(1980, 5, 14, 0, 0, 0, 0, time.UTC)) || !d.Expires.Equal(time.Date(2030, 1, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("dates: %v %v", d.DateOfBirth, d.Expires)
	}
	if short, ok := ParseDOCS("P/USA/X9"); !ok || short.Number != "X9" || !short.DateOfBirth.IsZero() {
		t.Fatalf("a DOCS with only the document still parses: %+v %v", short, ok)
	}
	if _, ok := ParseDOCS("P/GBR"); ok {
		t.Fatal("no document number, no document")
	}
	if _, ok := ParseDOCS(""); ok {
		t.Fatal("empty is nothing")
	}
}
