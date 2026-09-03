package bsp

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The handbook's own worked example (section 6.7.2, line 1): a cash sale
// with a fare of 1000 and tax of 10 has a document amount of 1010, an
// effective commission of 90 owed to the agent -- signed negative -- and
// a remittance of 911 (the handbook's line takes 9 of tax on commission
// as well; this package leaves tax on commission to bilateral schemes,
// so its remittance is the cash less the commission, 910).
func TestComputeFollowsTheHandbooksArithmetic(t *testing.T) {
	tx := Transaction{Code: TransSale, Fare: 1000, Taxes: []Tax{{Code: "GB", Amount: 10}}, CommissionRate: 900,
		Payments: []Payment{{Type: PaymentCash, Amount: 1010}}}
	tot := tx.Compute()
	if tx.Total != 1010 || tx.CommissionAmount != 90 || tx.Payments[0].Remittance != 920 {
		t.Errorf("sale: total %d commission %d remittance %d", tx.Total, tx.CommissionAmount, tx.Payments[0].Remittance)
	}
	if tot.Gross != 1010 || tot.Commission != -90 || tot.Taxes != 10 || tot.Remittance != 920 {
		t.Errorf("totals: %+v", tot)
	}
	// A card sale remits nothing in cash, and the commission still comes
	// off: the cash record is created with a negative remittance.
	card := Transaction{Code: TransSale, Fare: 1000, Taxes: []Tax{{Code: "GB", Amount: 10}}, CommissionRate: 900,
		Payments: []Payment{{Type: PaymentCard, Amount: 1010, Account: "XXXXXXXXXXXX1186"}}}
	card.Compute()
	if len(card.Payments) != 2 || card.Payments[1].Type != PaymentCash || card.Payments[1].Remittance != -90 {
		t.Errorf("card sale: %+v", card.Payments)
	}
}

// The over-punch table of section 2.4.6, verbatim: +0..+9 are { A-I, -0..-9
// are } J-R, in the least significant position.
func TestOverPunchMatchesTheTable(t *testing.T) {
	for v, want := range map[int64]string{0: "0000000000{", 1: "0000000000A", 9: "0000000000I", 10: "0000000001{",
		-1: "0000000000J", -9: "0000000000R", -10: "0000000001}", 98600: "0000009860{", 347800: "0000034780{", -34780: "0000003478}"} {
		rec := []byte(strings.Repeat(" ", 20))
		putAmount(rec, 1, 11, v)
		got := string(rec[:11])
		if got != want {
			t.Errorf("%d -> %q, want %q", v, got, want)
		}
		if back := getAmount(string(rec), 1, 11); back != v {
			t.Errorf("%q reads back as %d, want %d", got, back, v)
		}
	}
	// The handbook's net-remit example values: 0000009860{ is +98600 and
	// 0000009860} is -98600.
	if getAmount("0000009860{", 1, 11) != 98600 || getAmount("0000009860}", 1, 11) != -98600 {
		t.Error("the handbook's remittance examples do not read")
	}
}

func sampleFile() *File {
	issued := time.Date(2026, 11, 20, 14, 5, 0, 0, time.UTC)
	return &File{
		BSP: "LON", Airline: "125", Country: "GB", Processed: time.Date(2026, 11, 23, 3, 30, 0, 0, time.UTC), Sequence: 41,
		Cycle: Cycle{ProcessingWeek: "114", Number: 1, Ending: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), Final: true},
		Offices: []Office{{
			Agent: "91234562", RemittanceEnd: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), Currency: "GBP2",
			Transactions: []Transaction{{
				Code: TransSale, Issued: issued, Document: "1252400123456", CheckDigit: 6, Coupons: "FF", Agent: "91234562", ReportingSystem: "GDSL",
				Locator: "ABC123/1G", Origin: "LHR", Destination: "JFK", Fare: 343500, Taxes: []Tax{{"GB", 1300}, {"YQ", 1000}, {"XX", 500}, {"OC", 1500}},
				CommissionRate: 100, FareText: "GBP3435.00", TotalText: "GBP3478.00", TicketingMode: "/", ServicingSystem: "1251",
				Passenger: "SMITH/JOHN MR", PassengerType: "ADT",
				Segments: []Segment{
					{Coupon: 1, Stopover: "O", Origin: "LHR", Destination: "JFK", Carrier: "BA", Cabin: "Y", Flight: "0117", Class: "Y", Departs: time.Date(2026, 12, 16, 9, 0, 0, 0, time.UTC), DepartsHasTime: true, Status: "OK", Baggage: "1PC", FareBasis: "YOW", Equipment: "789"},
					{Coupon: 2, Stopover: "O", Origin: "JFK", Destination: "LHR", Carrier: "BA", Cabin: "Y", Flight: "0112", Class: "Y", Departs: time.Date(2026, 12, 23, 18, 30, 0, 0, time.UTC), DepartsHasTime: true, Status: "OK", Baggage: "1PC", FareBasis: "YOW", Equipment: "789"},
				},
				Payments: []Payment{{Type: PaymentCash, Amount: 347800}},
			}},
		}},
	}
}

// Column by column against the chapter 6 layouts: identifiers, sequence
// numbers, the amounts record's fields, the commission, a coupon, the
// passenger and the cash record with the remittance.
func TestWriteLaysRecordsOutToTheHandbook(t *testing.T) {
	var buf bytes.Buffer
	if err := sampleFile().Write(&buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	ids := make([]string, len(lines))
	for i, l := range lines {
		if len(l) != RecordLen {
			t.Errorf("record %d is %d characters", i+1, len(l))
		}
		ids[i] = l[:3] + l[11:13]
		if got := l[3:11]; got != strings.Repeat("0", 8-len(itoa(i+1)))+itoa(i+1) {
			t.Errorf("record %d sequence %q", i+1, got)
		}
	}
	want := []string{"BFH01", "BCH02", "BOH03", "BKT06", "BKS24", "BKS30", "BKS30", "BKS39", "BKI63", "BKI63", "BAR64", "BAR65", "BKP84", "BOT93", "BOT94", "BCT95", "BFT99"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("record order:\n got %v\nwant %v", ids, want)
	}
	col := func(rec int, from, to int) string { return lines[rec-1][from-1 : to] }
	// BFH01: BSP, airline, revision 230, PROD, date, time, country, sequence.
	if col(1, 14, 16) != "LON" || col(1, 17, 19) != "125" || col(1, 20, 22) != "230" || col(1, 23, 26) != "PROD" || col(1, 27, 32) != "261123" || col(1, 33, 36) != "0330" || col(1, 37, 38) != "GB" || col(1, 39, 44) != "000041" {
		t.Errorf("BFH01: %q", lines[0])
	}
	if col(2, 14, 16) != "114" || col(2, 17, 17) != "1" || col(2, 18, 23) != "261122" || col(2, 24, 24) != "F" {
		t.Errorf("BCH02: %q", lines[1])
	}
	if col(3, 14, 21) != "91234562" || col(3, 22, 27) != "261122" || col(3, 28, 31) != "GBP2" {
		t.Errorf("BOH03: %q", lines[2])
	}
	// BKT06: transaction 1, thirteen records BKT06..BKP84 (the cash record
	// included), airline 125, GDSL.
	if col(4, 14, 19) != "000001" || col(4, 22, 24) != "010" || col(4, 25, 27) != "125" || col(4, 65, 68) != "GDSL" {
		t.Errorf("BKT06: %q", lines[3])
	}
	if col(5, 14, 19) != "261120" || col(5, 20, 25) != "000001" || col(5, 26, 39) != "1252400123456 " || col(5, 40, 40) != "6" || col(5, 41, 44) != "FF  " || col(5, 48, 55) != "91234562" || col(5, 72, 75) != "TKTT" || col(5, 76, 85) != "LHR  JFK  " || col(5, 86, 98) != "ABC123/1G    " || col(5, 99, 102) != "1405" {
		t.Errorf("BKS24: %q", lines[4])
	}
	// BKS30, first: COBL 343500 -> 0000034350{, three taxes, TDAM 347800.
	if col(6, 41, 51) != "0000034350{" || col(6, 63, 70) != "GB      " || col(6, 71, 81) != "0000000130{" || col(6, 82, 89) != "YQ      " || col(6, 90, 100) != "0000000100{" || col(6, 101, 108) != "XX      " || col(6, 109, 119) != "0000000050{" || col(6, 120, 130) != "0000034780{" || col(6, 133, 136) != "GBP2" {
		t.Errorf("BKS30 first: %q", lines[5])
	}
	// The fourth tax spills onto a second amounts record with zero amounts.
	if col(7, 41, 51) != "0000000000{" || col(7, 63, 70) != "OC      " || col(7, 71, 81) != "0000000150{" || col(7, 120, 130) != "0000000000{" {
		t.Errorf("BKS30 second: %q", lines[6])
	}
	// BKS39: 1% of 343500 is 3435, owed to the agent, so negative: 000000343N.
	if col(8, 50, 54) != "00100" || col(8, 55, 65) != "0000000343N" || col(8, 88, 92) != "00100" || col(8, 93, 103) != "0000000343N" {
		t.Errorf("BKS39: %q", lines[7])
	}
	// BKI63 coupon 1.
	if col(9, 41, 41) != "1" || col(9, 42, 42) != "O" || col(9, 53, 57) != "LHR  " || col(9, 58, 62) != "JFK  " || col(9, 63, 65) != "BA " || col(9, 66, 66) != "Y" || col(9, 67, 71) != "0117 " || col(9, 72, 73) != "Y " || col(9, 74, 80) != "16DEC26" || col(9, 81, 85) != "0900 " || col(9, 86, 87) != "OK" || col(9, 88, 90) != "1PC" || col(9, 91, 105) != "YOW            " || col(9, 130, 132) != "789" {
		t.Errorf("BKI63: %q", lines[8])
	}
	if col(11, 41, 52) != "GBP3435.00  " || col(11, 53, 53) != "/" || col(11, 66, 77) != "GBP3478.00  " || col(11, 78, 81) != "1251" {
		t.Errorf("BAR64: %q", lines[10])
	}
	if col(12, 41, 53) != "SMITH/JOHN MR" || col(12, 126, 128) != "ADT" {
		t.Errorf("BAR65: %q", lines[11])
	}
	// BKP84-CA: paid 347800, remittance 347800 - 3435 = 344365.
	if col(13, 26, 35) != "CA        " || col(13, 36, 46) != "0000034780{" || col(13, 98, 108) != "0000034436E" || col(13, 133, 136) != "GBP2" {
		t.Errorf("BKP84: %q", lines[12])
	}
	// Totals: BOT93 per TKTT, BOT94, BCT95 and BFT99 all carry the sums.
	for _, r := range []int{14, 15} {
		if col(r, 14, 21) != "91234562" || col(r, 28, 42) != "00000000034780{" || col(r, 43, 57) != "00000000034436E" || col(r, 58, 72) != "00000000000343N" || col(r, 73, 87) != "00000000000430{" {
			t.Errorf("record %d totals: %q", r, lines[r-1])
		}
	}
	if col(14, 88, 91) != "TKTT" || col(14, 133, 136) != "GBP2" {
		t.Errorf("BOT93 code/currency: %q", lines[13])
	}
	if col(16, 18, 22) != "00001" || col(16, 23, 37) != "00000000034780{" || col(17, 14, 16) != "LON" || col(17, 17, 21) != "00001" || col(17, 22, 36) != "00000000034780{" {
		t.Errorf("BCT95/BFT99: %q / %q", lines[15], lines[16])
	}
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(n)) }

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestHOTRoundTrip(t *testing.T) {
	want := sampleFile()
	var buf bytes.Buffer
	if err := want.Write(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.BSP != "LON" || got.Airline != "125" || got.Country != "GB" || got.Sequence != 41 || got.Test || !got.Cycle.Final || got.Cycle.ProcessingWeek != "114" {
		t.Errorf("header: %+v", got)
	}
	if len(got.Offices) != 1 || got.Offices[0].Agent != "91234562" || got.Offices[0].Currency != "GBP2" || len(got.Offices[0].Transactions) != 1 {
		t.Fatalf("offices: %+v", got.Offices)
	}
	w, g := want.Offices[0].Transactions[0], got.Offices[0].Transactions[0]
	if g.Code != w.Code || g.Document != w.Document || g.CheckDigit != w.CheckDigit || g.Coupons != w.Coupons || g.Agent != w.Agent || g.Locator != w.Locator ||
		g.Origin != "LHR" || g.Destination != "JFK" || g.Fare != w.Fare || g.Total != 347800 || g.CommissionRate != 100 || g.CommissionAmount != 3435 ||
		g.Passenger != w.Passenger || g.PassengerType != "ADT" || g.FareText != w.FareText || g.TotalText != w.TotalText || g.ServicingSystem != "1251" || g.Currency != "GBP2" {
		t.Errorf("transaction\n want %+v\n got  %+v", w, g)
	}
	if !g.Issued.Equal(w.Issued) {
		t.Errorf("issued %v", g.Issued)
	}
	if len(g.Taxes) != 4 || g.Taxes[3] != (Tax{"OC", 1500}) {
		t.Errorf("taxes %+v", g.Taxes)
	}
	if len(g.Segments) != 2 || g.Segments[1].Flight != "0112" || !g.Segments[1].Departs.Equal(w.Segments[1].Departs) || g.Segments[0].Baggage != "1PC" || g.Segments[1].Equipment != "789" {
		t.Errorf("segments %+v", g.Segments)
	}
	if len(g.Payments) != 1 || g.Payments[0].Type != "CA" || g.Payments[0].Amount != 347800 || g.Payments[0].Remittance != 344365 {
		t.Errorf("payments %+v", g.Payments)
	}
	tot := got.Offices[0].OfficeTotals()
	if tot.Gross != 347800 || tot.Remittance != 344365 || tot.Commission != -3435 || tot.Taxes != 4300 {
		t.Errorf("office totals %+v", tot)
	}
}

func TestParseChecksCountsAndKeepsFragments(t *testing.T) {
	var buf bytes.Buffer
	sampleFile().Write(&buf)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// A record type the package does not lay out rides along as a fragment.
	extra := []byte(strings.Repeat(" ", RecordLen))
	copy(extra, "BKF")
	copy(extra[11:], "81")
	copy(extra[13:], "FARE CALCULATION HERE")
	// Insert before the BAR64 and renumber.
	var out []string
	seq := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "BAR") && l[11:13] == "64" {
			seq++
			out = append(out, renumber(string(extra), seq))
		}
		seq++
		out = append(out, renumber(l, seq))
	}
	// The transaction header still says ten records; with the fragment it is eleven.
	f, err := Parse(strings.NewReader(strings.Join(out, "\n") + "\n"))
	if err == nil {
		t.Fatalf("a record count that disagrees with the header parsed: %+v", f)
	}
	// Fix the count and the fragment is kept.
	for i, l := range out {
		if strings.HasPrefix(l, "BKT") {
			out[i] = l[:21] + "011" + l[24:]
		}
	}
	f, err = Parse(strings.NewReader(strings.Join(out, "\n") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Fragments) != 1 || !strings.HasPrefix(f.Fragments[0], "BKF") {
		t.Errorf("fragments %v", f.Fragments)
	}
	// A broken sequence is refused.
	out[5] = renumber(out[5], 99)
	if _, err := Parse(strings.NewReader(strings.Join(out, "\n") + "\n")); err == nil {
		t.Error("a sequence gap parsed")
	}
	if _, err := Parse(strings.NewReader("")); err == nil {
		t.Error("an empty file parsed")
	}
}

func renumber(l string, seq int) string {
	s := fmtInt(seq)
	return l[:3] + strings.Repeat("0", 8-len(s)) + s + l[11:]
}

func TestCheckDigitIsModulusSeven(t *testing.T) {
	// 1252400123456 mod 7: the unweighted method.
	got, err := CheckDigit("1252400123456")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range "1252400123456" {
		n = (n*10 + int(r-'0')) % 7
	}
	if got != n || got != 6 {
		t.Errorf("check digit %d, want %d", got, n)
	}
	if _, err := CheckDigit("12A"); err == nil {
		t.Error("letters accepted")
	}
}

func FuzzHOTRoundTrip(f *testing.F) {
	f.Add("SMITH/JOHN MR", "ABC123/1G", int64(343500), int64(1300), 900)
	f.Fuzz(func(t *testing.T, pax, loc string, fare, tax int64, rate int) {
		if fare < 0 || fare > 99999999999 || tax < 0 || tax > 9999999999 || rate < 0 || rate > 99999 {
			return
		}
		in := sampleFile()
		tx := &in.Offices[0].Transactions[0]
		tx.Passenger, tx.Locator, tx.Fare, tx.Taxes, tx.CommissionRate, tx.Total = pax, loc, fare, []Tax{{"GB", tax}}, rate, 0
		tx.Payments = []Payment{{Type: PaymentCash, Amount: fare + tax}}
		var buf bytes.Buffer
		if err := in.Write(&buf); err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if len(l) != RecordLen {
				t.Fatalf("record %d is %d characters: %q", i, len(l), l)
			}
		}
		out, err := Parse(&buf)
		if err != nil {
			t.Fatalf("own output does not parse: %v", err)
		}
		g := out.Offices[0].Transactions[0]
		if g.Fare != fare || g.Total != fare+tax || len(g.Taxes) != 1 || g.Taxes[0].Amount != tax {
			t.Errorf("amounts: fare %d total %d taxes %+v", g.Fare, g.Total, g.Taxes)
		}
		if g.CommissionAmount != tx.CommissionAmount || g.Payments[0].Remittance != fare+tax-tx.CommissionAmount {
			t.Errorf("commission %d remittance %d", g.CommissionAmount, g.Payments[0].Remittance)
		}
	})
}

// An exchange: the new document names the one it replaced in the
// qualifying-issue record, and the old document's value is its form of
// payment, so nothing new is remitted in cash.
func TestExchangeCarriesTheOriginalIssue(t *testing.T) {
	f := sampleFile()
	tx := &f.Offices[0].Transactions[0]
	tx.OriginalDocument, tx.OriginalLocation, tx.OriginalAgent = "1252400123400", "LON", "91234562"
	tx.OriginalIssued = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	tx.CommissionRate, tx.CommissionAmount = 0, 0
	tx.Payments = []Payment{{Type: PaymentExchange, Amount: 347800}}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	var bks46, ex, ca string
	for _, l := range lines {
		switch l[:3] + l[11:13] {
		case "BKS46":
			bks46 = l
		case "BKP84":
			if strings.HasPrefix(l[25:35], "EX") {
				ex = l
			} else {
				ca = l
			}
		}
	}
	if bks46 == "" || bks46[40:54] != "1252400123400 " || bks46[54:57] != "LON" || bks46[57:64] != "10NOV26" || bks46[64:72] != "91234562" {
		t.Errorf("BKS46: %q", bks46)
	}
	if ex == "" || ex[35:46] != "0000034780{" || ca == "" || ca[35:46] != "0000000000{" || ca[97:108] != "0000000000{" {
		t.Errorf("payments: EX %q CA %q", ex, ca)
	}
	back, err := Parse(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	g := back.Offices[0].Transactions[0]
	if g.OriginalDocument != "1252400123400" || g.OriginalLocation != "LON" || !g.OriginalIssued.Equal(tx.OriginalIssued) || g.OriginalAgent != "91234562" || len(g.Payments) != 2 || g.Payments[0].Type != "EX" {
		t.Errorf("back: %+v", g)
	}
}
