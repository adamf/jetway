// Package bsp is the settlement side of the ticket: the Airline
// Accounting/Sales data file -- the HOT -- that a Billing and Settlement
// Plan hands each airline for the documents agents sold on its behalf, and
// that the airline's revenue accounting reconciles against its own sales.
//
// This package is specified. IATA publishes the BSP Data Interchange
// Specifications Handbook (DISH, revision 23) free of charge, and the
// records here follow its chapter 6 layouts column by column: the file,
// cycle and office headers, the transaction header, the document
// identification, amounts and commission records, the itinerary segments,
// the passenger and form-of-payment records, and the office, cycle and
// file totals. Amounts are signed by the handbook's over-punch convention
// and add up the way its section 6.7 says they must. What the handbook
// leaves to bilateral agreement -- net reporting, card data, taxes on
// commission -- is left out; a record type this package does not lay out is
// kept verbatim as a fragment when read.
package bsp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// RecordLen is the fixed length of a HOT record.
const RecordLen = 136

// Transaction codes (TRNC), from the handbook's glossary.
const (
	TransSale   = "TKTT" // electronic ticketing sale, automated
	TransRefund = "RFND"
	TransCancel = "CANX"
	TransEMDA   = "EMDA"
	TransEMDS   = "EMDS"
	TransADM    = "ADMA"
	TransACM    = "ACMA"
)

// Form of payment types (FPTP). There is always one CA record, even when
// its amounts are zero; an exchange carries the exchanged document's value
// as its own form of payment, EX.
const (
	PaymentCash     = "CA"
	PaymentCard     = "CC"
	PaymentExchange = "EX"
)

// File is one HOT: a file header, one billing cycle, and the reporting
// offices with their transactions and totals.
type File struct {
	// BSP is the plan's city code (BSPI); Airline the ticketing airline's
	// three-digit code (TACN); Country the ISO country of the agents.
	BSP, Airline, Country string
	// Test marks a test file; production files say PROD.
	Test bool
	// Processed is when the plan produced the file; Sequence its file
	// sequence number (FSQN), which increments even for an empty file.
	Processed time.Time
	Sequence  int
	// Cycle describes the billing period.
	Cycle Cycle
	// Offices are the reporting agents, each with its transactions.
	Offices []Office
	// Fragments holds records of types this package does not lay out,
	// verbatim, in the order read.
	Fragments []string
}

// Cycle is the billing analysis (cycle) header: which period, which run.
type Cycle struct {
	// ProcessingWeek is MMW, the month and week within it (PDAI); Number
	// the processing cycle (PCYC), 1 by default.
	ProcessingWeek string
	Number         int
	// Ending is the billing analysis ending date (BAED); ReportingEnd the
	// last date of issue the file covers (HRED).
	Ending, ReportingEnd time.Time
	// Final marks the final run of the period (DYRI F); otherwise D.
	Final bool
}

// Office is one reporting agent: its IATA numeric code, its remittance
// period and currency, and what it sold.
type Office struct {
	// Agent is the eight-digit agent numeric code (seven digits and a
	// modulus-7 check digit).
	Agent string
	// RemittanceEnd is the last day of the agent's remittance period.
	RemittanceEnd time.Time
	// Currency is the currency type (CUTP): ISO code plus decimals, "USD2".
	Currency     string
	Transactions []Transaction
}

// Transaction is one accountable sale, refund or memo: a document with
// its amounts, commission, itinerary, passenger and payments.
type Transaction struct {
	Code string // TRNC
	// Issued is the date (and time, when known) of issue.
	Issued time.Time
	// Document is the ticket number: three-digit airline code and ten-digit
	// serial, fourteen characters with the form code; CheckDigit its
	// modulus-7 check digit. Coupons is the coupon use indicator, one
	// character per coupon position: F for a flight coupon, blank for
	// none.
	Document   string
	CheckDigit int
	Coupons    string
	// Agent is the issuing agent's numeric code; ReportingSystem the GDS or
	// system that reported it (RPSI).
	Agent           string
	ReportingSystem string
	// Locator is the PNR reference with the controlling system, "ABC123/1G".
	Locator string
	// Origin and Destination are the true origin and destination cities.
	Origin, Destination string
	// Currency is the currency type (CUTP) the amounts are in.
	Currency string
	// Fare is the commissionable amount (COBL): the fare paid, in minor
	// units. Taxes are the taxes, fees and charges, up to three per
	// amounts record. Total is the document amount (TDAM), fare plus
	// payable taxes.
	Fare  int64
	Taxes []Tax
	Total int64
	// FareText and TotalText are the document's own fare and total boxes
	// (FARE, TOTL): "USD 123.00" as printed, with the currency code.
	FareText, TotalText string
	// Commission is the standard commission: rate in hundredths of a
	// percent (10.5% is 1050) and amount in minor units.
	CommissionRate   int
	CommissionAmount int64
	// Segments are the flight coupons, up to four.
	Segments []Segment
	// Passenger is SURNAME/GIVEN TITLE as the document carries it; Type
	// the passenger type code.
	Passenger     string
	PassengerType string
	// Payments are the forms of payment; the cash record is always present
	// and carries the remittance.
	Payments []Payment
	// TicketingMode is "/" for a document generated by the carrier or a
	// CRS, "X" for one generated by an agent's own system from an
	// interface record.
	TicketingMode string
	// ServicingSystem is the airline or system provider code that made the
	// reservation (SASI), three digits and a check digit.
	ServicingSystem string
	// OriginalDocument, when set, marks an exchange: the document this one
	// was issued against, with where and when and by whom it was issued
	// (BKS46, qualifying issue information).
	OriginalDocument string
	OriginalIssued   time.Time
	OriginalLocation string
	OriginalAgent    string
}

// Tax is one tax, fee or charge on a document.
type Tax struct {
	Code   string // e.g. GB, YQ, XF
	Amount int64
}

// Segment is one flight coupon of the itinerary (BKI63).
type Segment struct {
	Coupon int
	// Stopover is X when no stopover is permitted at the destination, O
	// when one is.
	Stopover string
	// NotValidBefore and NotValidAfter are DDMMM, when stated.
	NotValidBefore, NotValidAfter string
	Origin, Destination           string
	Carrier                       string
	// Cabin is the sold cabin code; Flight the flight number; Class the
	// booking designator; Departs the date and, when known, local time.
	Cabin, Flight, Class string
	Departs              time.Time
	DepartsHasTime       bool
	// Status is the booking status, OK when confirmed.
	Status string
	// Baggage is the allowance, e.g. 1PC or 23K.
	Baggage   string
	FareBasis string
	Equipment string
}

// Payment is one form of payment (BKP84).
type Payment struct {
	Type   string // CA, CC...
	Amount int64
	// Account is the card number for a card payment, masked as the plan
	// masks it; Expiry MMYY; Approval the authorisation code.
	Account, Expiry, Approval string
	// Remittance is the amount due to the airline on the cash record: the
	// cash paid less the effective commission (and tax on commission).
	// Set by Compute; zero on every record but the cash one.
	Remittance int64
}

// Totals are the office, cycle and file sums the handbook defines: the
// document amounts (GROS), the remittances (TREM), the effective
// commission (TCOM), the taxes (TTMF) and the tax on commission (TTCA).
type Totals struct {
	Gross, Remittance, Commission, Taxes, TaxOnCommission int64
}

// Compute fills in what the handbook derives: the document amount from
// fare and taxes (6.7.1c), the remittance on the cash payment from cash
// less commission (6.7.1d), and a cash payment record when there is none.
// It returns the transaction's totals for the office subtotals.
func (t *Transaction) Compute() Totals {
	var taxes int64
	for _, tx := range t.Taxes {
		taxes += tx.Amount
	}
	if t.Total == 0 {
		t.Total = t.Fare + taxes
	}
	if t.CommissionAmount == 0 && t.CommissionRate != 0 {
		t.CommissionAmount = roundDiv(t.Fare*int64(t.CommissionRate), 10000)
	}
	cash := -1
	for i := range t.Payments {
		t.Payments[i].Remittance = 0
		if t.Payments[i].Type == PaymentCash {
			cash = i
		}
	}
	if cash < 0 {
		t.Payments = append(t.Payments, Payment{Type: PaymentCash})
		cash = len(t.Payments) - 1
	}
	// Commission is money the airline owes the agent: it is signed negative
	// on the file, and it comes off what the agent remits.
	t.Payments[cash].Remittance = t.Payments[cash].Amount - t.CommissionAmount
	return Totals{Gross: t.Total, Remittance: t.Payments[cash].Remittance, Commission: -t.CommissionAmount, Taxes: taxes}
}

func roundDiv(a, b int64) int64 {
	if a >= 0 {
		return (a + b/2) / b
	}
	return -((-a + b/2) / b)
}

// Write renders the file. Records are 136 characters, newline-terminated,
// numbered from 1 in the order written; each transaction is numbered from
// 1 within the file and counts its own records (TREC). Office subtotals
// are per transaction code; office, cycle and file totals per currency.
func (f *File) Write(w io.Writer) error {
	bw := bufio.NewWriter(w)
	seq := 0
	emit := func(rec []byte) {
		seq++
		put(rec, 4, 11, fmt.Sprintf("%08d", seq), true)
		bw.Write(rec)
		bw.WriteByte('\n')
	}
	rec := blank("BFH", 1)
	put(rec, 14, 16, f.BSP, false)
	put(rec, 17, 19, f.Airline, false)
	put(rec, 20, 22, "230", false)
	status := "PROD"
	if f.Test {
		status = "TEST"
	}
	put(rec, 23, 26, status, false)
	put(rec, 27, 32, yymmdd(f.Processed), false)
	put(rec, 33, 36, hhmm(f.Processed), false)
	put(rec, 37, 38, f.Country, false)
	put(rec, 39, 44, fmt.Sprintf("%06d", f.Sequence), false)
	emit(rec)

	rec = blank("BCH", 2)
	put(rec, 14, 16, f.Cycle.ProcessingWeek, false)
	put(rec, 17, 17, strconv.Itoa(max(f.Cycle.Number, 1)), false)
	put(rec, 18, 23, yymmdd(f.Cycle.Ending), false)
	run := "D"
	if f.Cycle.Final {
		run = "F"
	}
	put(rec, 24, 24, run, false)
	put(rec, 25, 30, yymmdd(orTime(f.Cycle.ReportingEnd, f.Cycle.Ending)), false)
	emit(rec)

	trnn := 0
	cycleTotals := map[string]*Totals{}
	var cycleCurrencies []string
	for oi := range f.Offices {
		o := &f.Offices[oi]
		rec = blank("BOH", 3)
		put(rec, 14, 21, o.Agent, true)
		put(rec, 22, 27, yymmdd(o.RemittanceEnd), false)
		put(rec, 28, 31, o.Currency, false)
		emit(rec)

		byCode := map[string]*Totals{}
		var codes []string
		officeTotals := map[string]*Totals{}
		var currencies []string
		for ti := range o.Transactions {
			t := &o.Transactions[ti]
			trnn++
			cur := t.Currency
			if cur == "" {
				cur = o.Currency
			}
			tot := t.Compute()
			recs := transactionRecords(t, trnn, cur)
			for _, r := range recs {
				emit(r)
			}
			key := t.Code + "|" + cur
			if byCode[key] == nil {
				byCode[key] = &Totals{}
				codes = append(codes, key)
			}
			byCode[key].add(tot)
			if officeTotals[cur] == nil {
				officeTotals[cur] = &Totals{}
				currencies = append(currencies, cur)
			}
			officeTotals[cur].add(tot)
			if cycleTotals[cur] == nil {
				cycleTotals[cur] = &Totals{}
				cycleCurrencies = append(cycleCurrencies, cur)
			}
			cycleTotals[cur].add(tot)
		}
		for _, key := range codes {
			code, cur, _ := strings.Cut(key, "|")
			tot := byCode[key]
			rec = blank("BOT", 93)
			put(rec, 14, 21, o.Agent, true)
			put(rec, 22, 27, yymmdd(o.RemittanceEnd), false)
			putAmount(rec, 28, 42, tot.Gross)
			putAmount(rec, 43, 57, tot.Remittance)
			putAmount(rec, 58, 72, tot.Commission)
			putAmount(rec, 73, 87, tot.Taxes)
			put(rec, 88, 91, code, false)
			putAmount(rec, 92, 106, tot.TaxOnCommission)
			put(rec, 133, 136, cur, false)
			emit(rec)
		}
		for _, cur := range currencies {
			tot := officeTotals[cur]
			rec = blank("BOT", 94)
			put(rec, 14, 21, o.Agent, true)
			put(rec, 22, 27, yymmdd(o.RemittanceEnd), false)
			putAmount(rec, 28, 42, tot.Gross)
			putAmount(rec, 43, 57, tot.Remittance)
			putAmount(rec, 58, 72, tot.Commission)
			putAmount(rec, 73, 87, tot.Taxes)
			putAmount(rec, 88, 102, tot.TaxOnCommission)
			put(rec, 133, 136, cur, false)
			emit(rec)
		}
	}
	if len(cycleCurrencies) == 0 {
		cycleCurrencies = []string{""}
		cycleTotals[""] = &Totals{}
	}
	for _, cur := range cycleCurrencies {
		tot := cycleTotals[cur]
		rec = blank("BCT", 95)
		put(rec, 14, 16, f.Cycle.ProcessingWeek, false)
		put(rec, 17, 17, strconv.Itoa(max(f.Cycle.Number, 1)), false)
		put(rec, 18, 22, fmt.Sprintf("%05d", len(f.Offices)), false)
		putAmount(rec, 23, 37, tot.Gross)
		putAmount(rec, 38, 52, tot.Remittance)
		putAmount(rec, 53, 67, tot.Commission)
		putAmount(rec, 68, 82, tot.Taxes)
		putAmount(rec, 83, 97, tot.TaxOnCommission)
		put(rec, 133, 136, cur, false)
		emit(rec)
	}
	for _, cur := range cycleCurrencies {
		tot := cycleTotals[cur]
		rec = blank("BFT", 99)
		put(rec, 14, 16, f.BSP, false)
		put(rec, 17, 21, fmt.Sprintf("%05d", len(f.Offices)), false)
		putAmount(rec, 22, 36, tot.Gross)
		putAmount(rec, 37, 51, tot.Remittance)
		putAmount(rec, 52, 66, tot.Commission)
		putAmount(rec, 67, 81, tot.Taxes)
		putAmount(rec, 82, 96, tot.TaxOnCommission)
		put(rec, 133, 136, cur, false)
		emit(rec)
	}
	return bw.Flush()
}

func (t *Totals) add(o Totals) {
	t.Gross += o.Gross
	t.Remittance += o.Remittance
	t.Commission += o.Commission
	t.Taxes += o.Taxes
	t.TaxOnCommission += o.TaxOnCommission
}

// transactionRecords lays one transaction out: BKT06, BKS24, BKS30 (one
// per three taxes), BKS39, BKI63 per coupon, BAR64, BAR65, BKP84 per form
// of payment with the cash record last, as the handbook's issue diagram
// orders them.
func transactionRecords(t *Transaction, trnn int, cur string) [][]byte {
	var out [][]byte
	doc := func(smsg string, stnq int) []byte {
		rec := blank(smsg, stnq)
		put(rec, 14, 19, yymmdd(t.Issued), false)
		put(rec, 20, 25, fmt.Sprintf("%06d", trnn), false)
		put(rec, 26, 39, t.Document, false)
		put(rec, 40, 40, strconv.Itoa(t.CheckDigit), false)
		return rec
	}
	head := blank("BKT", 6)
	put(head, 14, 19, fmt.Sprintf("%06d", trnn), false)
	put(head, 25, 27, t.Document[:min(3, len(t.Document))], false)
	put(head, 65, 68, t.ReportingSystem, false)
	out = append(out, head)

	rec := doc("BKS", 24)
	put(rec, 41, 44, t.Coupons, false)
	put(rec, 48, 55, t.Agent, true)
	put(rec, 72, 75, t.Code, false)
	put(rec, 76, 85, t.Origin+strings.Repeat(" ", 5-min(5, len(t.Origin)))+t.Destination, false)
	put(rec, 86, 98, t.Locator, false)
	if !t.Issued.IsZero() && (t.Issued.Hour() != 0 || t.Issued.Minute() != 0) {
		put(rec, 99, 102, hhmm(t.Issued), false)
	}
	out = append(out, rec)

	taxes := t.Taxes
	first := true
	for first || len(taxes) > 0 {
		rec = doc("BKS", 30)
		if first {
			putAmount(rec, 41, 51, t.Fare)
			putAmount(rec, 120, 130, t.Total)
		} else {
			putAmount(rec, 41, 51, 0)
			putAmount(rec, 120, 130, 0)
		}
		putAmount(rec, 52, 62, 0)
		for i, col := range []int{63, 82, 101} {
			if i < len(taxes) {
				put(rec, col, col+7, taxes[i].Code, false)
				putAmount(rec, col+8, col+18, taxes[i].Amount)
			} else {
				putAmount(rec, col+8, col+18, 0)
			}
		}
		if len(taxes) > 3 {
			taxes = taxes[3:]
		} else {
			taxes = nil
		}
		put(rec, 133, 136, cur, false)
		out = append(out, rec)
		first = false
	}

	rec = doc("BKS", 39)
	put(rec, 50, 54, fmt.Sprintf("%05d", t.CommissionRate), false)
	putAmount(rec, 55, 65, -t.CommissionAmount)
	put(rec, 72, 76, "00000", false)
	putAmount(rec, 77, 87, 0)
	put(rec, 88, 92, fmt.Sprintf("%05d", t.CommissionRate), false)
	putAmount(rec, 93, 103, -t.CommissionAmount)
	putAmount(rec, 104, 114, 0)
	put(rec, 133, 136, cur, false)
	out = append(out, rec)

	if t.OriginalDocument != "" {
		rec = doc("BKS", 46)
		put(rec, 41, 54, t.OriginalDocument, false)
		put(rec, 55, 57, t.OriginalLocation, false)
		if !t.OriginalIssued.IsZero() {
			put(rec, 58, 64, strings.ToUpper(t.OriginalIssued.Format("02Jan06")), false)
		}
		put(rec, 65, 72, t.OriginalAgent, true)
		out = append(out, rec)
	}
	for _, s := range t.Segments {
		rec = doc("BKI", 63)
		put(rec, 41, 41, strconv.Itoa(s.Coupon), false)
		put(rec, 42, 42, s.Stopover, false)
		put(rec, 43, 47, s.NotValidBefore, false)
		put(rec, 48, 52, s.NotValidAfter, false)
		put(rec, 53, 57, s.Origin, false)
		put(rec, 58, 62, s.Destination, false)
		put(rec, 63, 65, s.Carrier, false)
		put(rec, 66, 66, s.Cabin, false)
		put(rec, 67, 71, s.Flight, false)
		put(rec, 72, 73, s.Class, false)
		if !s.Departs.IsZero() {
			put(rec, 74, 80, strings.ToUpper(s.Departs.Format("02Jan06")), false)
			if s.DepartsHasTime {
				put(rec, 81, 85, s.Departs.Format("1504"), false)
			}
		}
		put(rec, 86, 87, s.Status, false)
		put(rec, 88, 90, s.Baggage, false)
		put(rec, 91, 105, s.FareBasis, false)
		put(rec, 130, 132, s.Equipment, false)
		out = append(out, rec)
	}

	rec = doc("BAR", 64)
	put(rec, 41, 52, t.FareText, false)
	put(rec, 53, 53, orElse(t.TicketingMode, "/"), false)
	put(rec, 66, 77, t.TotalText, false)
	put(rec, 78, 81, t.ServicingSystem, false)
	out = append(out, rec)

	rec = doc("BAR", 65)
	put(rec, 41, 89, t.Passenger, false)
	put(rec, 126, 128, t.PassengerType, false)
	out = append(out, rec)

	// Payments, the cash record last: the handbook's priority puts
	// exchange, card and credit-to-cash before it.
	var pays []Payment
	var cash *Payment
	for i := range t.Payments {
		if t.Payments[i].Type == PaymentCash && cash == nil {
			cash = &t.Payments[i]
			continue
		}
		pays = append(pays, t.Payments[i])
	}
	if cash != nil {
		pays = append(pays, *cash)
	}
	for _, p := range pays {
		rec = blank("BKP", 84)
		put(rec, 14, 19, yymmdd(t.Issued), false)
		put(rec, 20, 25, fmt.Sprintf("%06d", trnn), false)
		put(rec, 26, 35, p.Type, false)
		putAmount(rec, 36, 46, p.Amount)
		put(rec, 47, 65, p.Account, false)
		put(rec, 66, 69, p.Expiry, false)
		put(rec, 72, 77, p.Approval, false)
		putAmount(rec, 98, 108, p.Remittance)
		put(rec, 133, 136, cur, false)
		out = append(out, rec)
	}
	put(head, 22, 24, fmt.Sprintf("%03d", len(out)), false)
	return out
}

// Parse reads a HOT. It rebuilds the offices and their transactions from
// the records, checks each transaction's record count against its header
// and each record's sequence against its position, and keeps records of
// types it does not lay out as fragments. Amounts come back signed.
func Parse(r io.Reader) (*File, error) {
	f := &File{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	var (
		office *Office
		tx     *Transaction
		expect int
		count  int
		n      int
	)
	finishTx := func() error {
		if tx == nil {
			return nil
		}
		if expect != 0 && count != expect {
			return fmt.Errorf("bsp: transaction %s has %d records, its header said %d", tx.Document, count, expect)
		}
		office.Transactions = append(office.Transactions, *tx)
		tx = nil
		return nil
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		n++
		if len(line) < RecordLen {
			line += strings.Repeat(" ", RecordLen-len(line))
		}
		if seq := atoi(get(line, 4, 11)); seq != n {
			return nil, fmt.Errorf("bsp: record %d carries sequence %d", n, seq)
		}
		id := get(line, 1, 3) + get(line, 12, 13)
		if tx != nil && strings.HasPrefix(id, "BK") || tx != nil && strings.HasPrefix(id, "BAR") || tx != nil && id == "BKP84" {
			if id != "BKT06" {
				count++
			}
		}
		switch id {
		case "BFH01":
			f.BSP, f.Airline, f.Country = get(line, 14, 16), get(line, 17, 19), get(line, 37, 38)
			f.Test = get(line, 23, 26) == "TEST"
			f.Processed = parseYYMMDD(get(line, 27, 32), get(line, 33, 36))
			f.Sequence = atoi(get(line, 39, 44))
		case "BCH02":
			f.Cycle = Cycle{ProcessingWeek: get(line, 14, 16), Number: atoi(get(line, 17, 17)),
				Ending: parseYYMMDD(get(line, 18, 23), ""), Final: get(line, 24, 24) == "F", ReportingEnd: parseYYMMDD(get(line, 25, 30), "")}
		case "BOH03":
			if err := finishTx(); err != nil {
				return nil, err
			}
			f.Offices = append(f.Offices, Office{Agent: get(line, 14, 21), RemittanceEnd: parseYYMMDD(get(line, 22, 27), ""), Currency: get(line, 28, 31)})
			office = &f.Offices[len(f.Offices)-1]
		case "BKT06":
			if err := finishTx(); err != nil {
				return nil, err
			}
			if office == nil {
				return nil, fmt.Errorf("bsp: a transaction before any office header")
			}
			tx = &Transaction{ReportingSystem: get(line, 65, 68)}
			expect, count = atoi(get(line, 22, 24)), 1
		case "BKS24":
			if tx == nil {
				return nil, fmt.Errorf("bsp: BKS24 outside a transaction")
			}
			tx.Issued = parseYYMMDD(get(line, 14, 19), get(line, 99, 102))
			tx.Document, tx.CheckDigit = get(line, 26, 39), atoi(get(line, 40, 40))
			tx.Coupons = strings.TrimRight(line[40:44], " ")
			tx.Agent, tx.Code = get(line, 48, 55), get(line, 72, 75)
			tx.Origin, tx.Destination = get(line, 76, 80), get(line, 81, 85)
			tx.Locator = get(line, 86, 98)
		case "BKS30":
			if tx == nil {
				continue
			}
			if a := getAmount(line, 41, 51); a != 0 {
				tx.Fare = a
			}
			if a := getAmount(line, 120, 130); a != 0 {
				tx.Total = a
			}
			for _, col := range []int{63, 82, 101} {
				if code := get(line, col, col+7); code != "" {
					tx.Taxes = append(tx.Taxes, Tax{Code: code, Amount: getAmount(line, col+8, col+18)})
				}
			}
			tx.Currency = get(line, 133, 136)
		case "BKS39":
			if tx == nil {
				continue
			}
			tx.CommissionRate = atoi(get(line, 50, 54))
			tx.CommissionAmount = -getAmount(line, 55, 65)
		case "BKS46":
			if tx == nil {
				continue
			}
			tx.OriginalDocument, tx.OriginalLocation, tx.OriginalAgent = get(line, 41, 54), get(line, 55, 57), get(line, 65, 72)
			if d := get(line, 58, 64); len(d) == 7 {
				if t, err := time.Parse("02Jan06", d[:2]+strings.ToUpper(d[2:3])+strings.ToLower(d[3:5])+d[5:]); err == nil {
					tx.OriginalIssued = t
				}
			}
		case "BKI63":
			if tx == nil {
				continue
			}
			s := Segment{Coupon: atoi(get(line, 41, 41)), Stopover: get(line, 42, 42), NotValidBefore: get(line, 43, 47), NotValidAfter: get(line, 48, 52),
				Origin: get(line, 53, 57), Destination: get(line, 58, 62), Carrier: get(line, 63, 65), Cabin: get(line, 66, 66),
				Flight: strings.TrimLeft(get(line, 67, 71), " "), Class: get(line, 72, 73), Status: get(line, 86, 87), Baggage: get(line, 88, 90),
				FareBasis: get(line, 91, 105), Equipment: get(line, 130, 132)}
			if d := get(line, 74, 80); len(d) == 7 {
				if t, err := time.Parse("02Jan06", d[:2]+strings.ToUpper(d[2:3])+strings.ToLower(d[3:5])+d[5:]); err == nil {
					s.Departs = t
					if hm := get(line, 81, 85); len(hm) == 4 {
						if tt, err := time.Parse("1504", hm); err == nil {
							s.Departs = t.Add(time.Duration(tt.Hour())*time.Hour + time.Duration(tt.Minute())*time.Minute)
							s.DepartsHasTime = true
						}
					}
				}
			}
			tx.Segments = append(tx.Segments, s)
		case "BAR64":
			if tx == nil {
				continue
			}
			tx.FareText, tx.TicketingMode, tx.TotalText, tx.ServicingSystem = get(line, 41, 52), get(line, 53, 53), get(line, 66, 77), get(line, 78, 81)
		case "BAR65":
			if tx == nil {
				continue
			}
			tx.Passenger, tx.PassengerType = get(line, 41, 89), get(line, 126, 128)
		case "BKP84":
			if tx == nil {
				continue
			}
			tx.Payments = append(tx.Payments, Payment{Type: get(line, 26, 35), Amount: getAmount(line, 36, 46),
				Account: get(line, 47, 65), Expiry: get(line, 66, 69), Approval: get(line, 72, 77), Remittance: getAmount(line, 98, 108)})
		case "BOT93", "BOT94", "BCT95", "BFT99":
			if err := finishTx(); err != nil {
				return nil, err
			}
			// Totals are derived; a reader recomputes them from the
			// transactions and may compare (see Totals).
		default:
			f.Fragments = append(f.Fragments, strings.TrimRight(line, " "))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("bsp: read: %w", err)
	}
	if err := finishTx(); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("bsp: empty file")
	}
	return f, nil
}

// OfficeTotals sums an office's transactions the way BOT94 does, without
// changing them.
func (o Office) OfficeTotals() Totals {
	var tot Totals
	for _, t := range o.Transactions {
		var taxes int64
		for _, tx := range t.Taxes {
			taxes += tx.Amount
		}
		var rem int64
		for _, p := range t.Payments {
			rem += p.Remittance
		}
		tot.add(Totals{Gross: t.Total, Remittance: rem, Commission: -t.CommissionAmount, Taxes: taxes})
	}
	return tot
}

// CheckDigit is the unweighted modulus-7 check digit of a document number:
// the number modulo seven.
func CheckDigit(number string) (int, error) {
	digits := strings.TrimSpace(number)
	if digits == "" {
		return 0, fmt.Errorf("bsp: no number")
	}
	rem := 0
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("bsp: %q is not all digits", number)
		}
		rem = (rem*10 + int(r-'0')) % 7
	}
	return rem, nil
}

// blank is a record of spaces with its identifier: the standard message
// identifier in columns 1-3 and the numeric qualifier in 12-13.
func blank(smsg string, stnq int) []byte {
	rec := []byte(strings.Repeat(" ", RecordLen))
	put(rec, 1, 3, smsg, false)
	put(rec, 12, 13, fmt.Sprintf("%02d", stnq), false)
	return rec
}

// put writes s into 1-based inclusive columns, left-justified with
// trailing blanks or right-justified with leading zeros for numbers,
// truncating what does not fit. Control characters become spaces: a
// fixed-width record cannot carry them.
func put(rec []byte, start, end int, s string, numeric bool) {
	w := end - start + 1
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return ' '
		}
		return r
	}, s)
	if len(s) > w {
		s = s[:w]
	}
	if numeric {
		s = strings.Repeat("0", w-len(s)) + s
	}
	copy(rec[start-1:], s)
}

// putAmount writes a signed amount in the handbook's over-punch form: the
// digits right-justified with leading zeros, the sign carried in the last
// digit -- { A..I for +0..+9, } J..R for -0..-9. Zero is positive.
func putAmount(rec []byte, start, end int, v int64) {
	w := end - start + 1
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%0*d", w, v)
	if len(s) > w {
		s = s[len(s)-w:]
	}
	last := s[len(s)-1] - '0'
	var sign byte
	if neg {
		sign = "}JKLMNOPQR"[last]
	} else {
		sign = "{ABCDEFGHI"[last]
	}
	copy(rec[start-1:], s[:len(s)-1]+string(sign))
}

// getAmount reads an over-punched amount; plain digits read as positive.
func getAmount(line string, start, end int) int64 {
	s := line[start-1 : end]
	if s == "" {
		return 0
	}
	last := s[len(s)-1]
	body := s[:len(s)-1]
	neg := false
	var d byte
	switch {
	case last >= '0' && last <= '9':
		d = last - '0'
	case last == '{':
		d = 0
	case last >= 'A' && last <= 'I':
		d = last - 'A' + 1
	case last == '}':
		d, neg = 0, true
	case last >= 'J' && last <= 'R':
		d, neg = last-'J'+1, true
	default:
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(body)+string('0'+d), 10, 64)
	if neg {
		v = -v
	}
	return v
}

func get(line string, start, end int) string {
	if end > len(line) {
		end = len(line)
	}
	if start > end {
		return ""
	}
	return strings.TrimSpace(line[start-1 : end])
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func yymmdd(t time.Time) string {
	if t.IsZero() {
		return "000000"
	}
	return t.Format("060102")
}

func hhmm(t time.Time) string {
	if t.IsZero() {
		return "0000"
	}
	return t.Format("1504")
}

func parseYYMMDD(d, hm string) time.Time {
	if len(d) != 6 || d == "000000" {
		return time.Time{}
	}
	t, err := time.Parse("060102", d)
	if err != nil {
		return time.Time{}
	}
	if len(hm) == 4 {
		if tt, err := time.Parse("1504", hm); err == nil {
			t = t.Add(time.Duration(tt.Hour())*time.Hour + time.Duration(tt.Minute())*time.Minute)
		}
	}
	return t
}

func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func orTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}
