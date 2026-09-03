package bsp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The RET is the other half of the exchange with a settlement plan: the
// Agent Reporting Data file a reporting system -- a distribution system,
// an agent's own -- sends the plan for every accountable transaction its
// agents made, from which the plan builds each airline's HOT. Chapter 5 of
// the same handbook lays it out: 255-character records, a file header,
// then per transaction the basic record, monetary amounts, itinerary,
// fare calculation, forms of payment and additional information, and a
// trailer counting the records. Amounts here are unsigned (section 2.4.5:
// value fields on agent reporting are not signed), so a refund carries
// its amounts as positives under its transaction code.

// RETRecordLen is the fixed length of an agent reporting record.
const RETRecordLen = 255

// RET is one agent reporting file.
type RET struct {
	// PeriodEnd is the reporting period ending date (SPED); System the
	// reporting system identifier (RPSI); Country the ISO country code.
	PeriodEnd time.Time
	System    string
	Country   string
	Test      bool
	Processed time.Time
	// Sequence is the file type sequence number within the period (FTSN).
	Sequence     int
	Transactions []Transaction
	// Fragments holds records of types this package does not lay out.
	Fragments []string
}

// Write renders the file with transactions numbered from 1.
func (f *RET) Write(w io.Writer) error {
	bw := bufio.NewWriter(w)
	n := 0
	emit := func(rec []byte) {
		n++
		bw.Write(rec)
		bw.WriteByte('\n')
	}
	rec := retBlank("1")
	putRET(rec, 2, 7, yymmdd(f.PeriodEnd), false)
	putRET(rec, 8, 11, f.System, false)
	putRET(rec, 12, 14, "230", false)
	status := "PROD"
	if f.Test {
		status = "TEST"
	}
	putRET(rec, 15, 18, status, false)
	putRET(rec, 19, 24, yymmdd(f.Processed), false)
	putRET(rec, 25, 28, hhmm(f.Processed), false)
	putRET(rec, 29, 30, f.Country, false)
	if f.Sequence > 0 {
		putRET(rec, 31, 32, fmt.Sprintf("%02d", f.Sequence), false)
	}
	emit(rec)
	for i := range f.Transactions {
		for _, r := range retTransaction(&f.Transactions[i], i+1, f.Country) {
			emit(r)
		}
	}
	rec = retBlank("Z")
	putRET(rec, 2, 12, fmt.Sprintf("%011d", n+1), false)
	emit(rec)
	return bw.Flush()
}

func retTransaction(t *Transaction, trnn int, country string) [][]byte {
	var out [][]byte
	num := fmt.Sprintf("%06d", trnn)
	rec := retBlank("2")
	putRET(rec, 2, 7, num, false)
	putRET(rec, 8, 15, t.Agent, true)
	putRET(rec, 19, 22, t.Coupons, false)
	putRET(rec, 23, 28, yymmdd(t.Issued), false)
	putRET(rec, 32, 45, t.Document, false)
	putRET(rec, 46, 46, strconv.Itoa(t.CheckDigit), false)
	putRET(rec, 47, 50, t.Code, false)
	acct := t.Document
	if len(acct) > 3 {
		acct = acct[:3]
	}
	putRET(rec, 51, 53, acct, false)
	if cd, err := CheckDigit(acct); err == nil {
		putRET(rec, 54, 54, strconv.Itoa(cd), false)
	} else {
		putRET(rec, 54, 54, "9", false)
	}
	putRET(rec, 55, 103, t.Passenger, false)
	putRET(rec, 119, 120, country, false)
	putRET(rec, 122, 134, t.Locator, false)
	putRET(rec, 135, 144, t.Origin+strings.Repeat(" ", 5-min(5, len(t.Origin)))+t.Destination, false)
	putRET(rec, 145, 145, orElse(t.TicketingMode, "/"), false)
	putRET(rec, 161, 164, t.ServicingSystem, false)
	putRET(rec, 208, 210, t.PassengerType, false)
	if !t.Issued.IsZero() && (t.Issued.Hour() != 0 || t.Issued.Minute() != 0) {
		putRET(rec, 213, 216, hhmm(t.Issued), false)
	}
	out = append(out, rec)

	// Monetary amounts: the document amount, up to six taxes, the
	// commission. Unsigned; a refund's code says what the amounts mean.
	taxes := t.Taxes
	first := true
	for first || len(taxes) > 0 {
		rec = retBlank("5")
		putRET(rec, 2, 7, num, false)
		if first {
			putRET(rec, 8, 18, fmt.Sprintf("%011d", abs64(t.Total)), false)
		} else {
			putRET(rec, 8, 18, fmt.Sprintf("%011d", 0), false)
		}
		putRET(rec, 19, 22, t.Currency, false)
		for i, col := range []int{23, 42, 61, 80, 99, 118} {
			if i < len(taxes) {
				putRET(rec, col, col+7, taxes[i].Code, false)
				putRET(rec, col+8, col+18, fmt.Sprintf("%011d", abs64(taxes[i].Amount)), false)
			} else {
				putRET(rec, col+8, col+18, fmt.Sprintf("%011d", 0), false)
			}
		}
		if len(taxes) > 6 {
			taxes = taxes[6:]
		} else {
			taxes = nil
		}
		if first {
			putRET(rec, 143, 147, fmt.Sprintf("%05d", t.CommissionRate), false)
			putRET(rec, 148, 158, fmt.Sprintf("%011d", abs64(t.CommissionAmount)), false)
		} else {
			putRET(rec, 143, 147, "00000", false)
			putRET(rec, 148, 158, fmt.Sprintf("%011d", 0), false)
		}
		putRET(rec, 165, 169, "00000", false)
		putRET(rec, 170, 180, fmt.Sprintf("%011d", 0), false)
		putRET(rec, 187, 191, "00000", false)
		putRET(rec, 192, 202, fmt.Sprintf("%011d", 0), false)
		putRET(rec, 205, 215, fmt.Sprintf("%011d", 0), false)
		putRET(rec, 216, 226, fmt.Sprintf("%011d", 0), false)
		putRET(rec, 233, 243, fmt.Sprintf("%011d", 0), false)
		out = append(out, rec)
		first = false
	}

	// Itinerary: two segments per record.
	for i := 0; i < len(t.Segments); i += 2 {
		rec = retBlank("6")
		putRET(rec, 2, 7, num, false)
		for j := 0; j < 2 && i+j < len(t.Segments); j++ {
			s := t.Segments[i+j]
			off := j * 124
			putRET(rec, 8+off, 12+off, s.Origin, false)
			putRET(rec, 13+off, 17+off, s.Destination, false)
			putRET(rec, 38+off, 40+off, s.Carrier, false)
			putRET(rec, 41+off, 41+off, s.Cabin, false)
			putRET(rec, 42+off, 43+off, s.Class, false)
			if !s.Departs.IsZero() {
				putRET(rec, 44+off, 50+off, strings.ToUpper(s.Departs.Format("02Jan06")), false)
				if s.DepartsHasTime {
					putRET(rec, 81+off, 85+off, s.Departs.Format("1504"), false)
				}
			}
			putRET(rec, 51+off, 55+off, s.NotValidBefore, false)
			putRET(rec, 56+off, 60+off, s.NotValidAfter, false)
			putRET(rec, 61+off, 75+off, s.FareBasis, false)
			putRET(rec, 76+off, 80+off, s.Flight, false)
			putRET(rec, 86+off, 88+off, s.Baggage, false)
			putRET(rec, 89+off, 90+off, s.Status, false)
			putRET(rec, 91+off, 91+off, strconv.Itoa(s.Coupon), false)
			putRET(rec, 92+off, 92+off, s.Stopover, false)
			putRET(rec, 97+off, 99+off, s.Equipment, false)
		}
		out = append(out, rec)
	}

	rec = retBlank("7")
	putRET(rec, 2, 7, num, false)
	putRET(rec, 8, 19, t.FareText, false)
	putRET(rec, 32, 43, t.TotalText, false)
	if t.OriginalDocument != "" {
		putRET(rec, 222, 235, t.OriginalDocument, false)
		putRET(rec, 236, 238, t.OriginalLocation, false)
		if !t.OriginalIssued.IsZero() {
			putRET(rec, 239, 245, strings.ToUpper(t.OriginalIssued.Format("02Jan06")), false)
		}
		putRET(rec, 246, 253, t.OriginalAgent, true)
	}
	out = append(out, rec)

	for _, p := range t.Payments {
		rec = retBlank("8")
		putRET(rec, 2, 7, num, false)
		putRET(rec, 8, 26, p.Account, false)
		putRET(rec, 27, 37, fmt.Sprintf("%011d", abs64(p.Amount)), false)
		putRET(rec, 38, 43, p.Approval, false)
		putRET(rec, 44, 47, t.Currency, false)
		putRET(rec, 50, 59, p.Type, false)
		putRET(rec, 60, 63, p.Expiry, false)
		out = append(out, rec)
	}

	rec = retBlank("9")
	putRET(rec, 2, 7, num, false)
	if len(t.Payments) > 0 {
		putRET(rec, 155, 204, t.Payments[0].Type, false)
	}
	out = append(out, rec)
	return out
}

// ParseRET reads an agent reporting file into transactions, grouped by
// transaction number. Records of types this package does not lay out are
// kept as fragments; amounts are read as the unsigned values the file
// carries.
func ParseRET(r io.Reader) (*RET, error) {
	f := &RET{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	byNum := map[string]*Transaction{}
	var order []string
	n := 0
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		n++
		if len(line) < RETRecordLen {
			line += strings.Repeat(" ", RETRecordLen-len(line))
		}
		id := line[0]
		if id == '1' {
			f.PeriodEnd = parseYYMMDD(get(line, 2, 7), "")
			f.System, f.Country = get(line, 8, 11), get(line, 29, 30)
			f.Test = get(line, 15, 18) == "TEST"
			f.Processed = parseYYMMDD(get(line, 19, 24), get(line, 25, 28))
			f.Sequence = atoi(get(line, 31, 32))
			continue
		}
		if id == 'Z' {
			continue
		}
		num := get(line, 2, 7)
		tx := byNum[num]
		if tx == nil {
			tx = &Transaction{}
			byNum[num] = tx
			order = append(order, num)
		}
		switch id {
		case '2':
			tx.Agent, tx.Coupons = get(line, 8, 15), strings.TrimRight(line[18:22], " ")
			tx.Issued = parseYYMMDD(get(line, 23, 28), get(line, 213, 216))
			tx.Document, tx.CheckDigit, tx.Code = get(line, 32, 45), atoi(get(line, 46, 46)), get(line, 47, 50)
			tx.Passenger, tx.Locator = get(line, 55, 103), get(line, 122, 134)
			tx.Origin, tx.Destination = get(line, 135, 139), get(line, 140, 144)
			tx.TicketingMode, tx.ServicingSystem, tx.PassengerType = get(line, 145, 145), get(line, 161, 164), get(line, 208, 210)
		case '5':
			if a := atoi64(get(line, 8, 18)); a != 0 {
				tx.Total = a
			}
			tx.Currency = get(line, 19, 22)
			for _, col := range []int{23, 42, 61, 80, 99, 118} {
				if code := get(line, col, col+7); code != "" {
					tx.Taxes = append(tx.Taxes, Tax{Code: code, Amount: atoi64(get(line, col+8, col+18))})
				}
			}
			if r := atoi(get(line, 143, 147)); r != 0 {
				tx.CommissionRate = r
			}
			if a := atoi64(get(line, 148, 158)); a != 0 {
				tx.CommissionAmount = a
			}
		case '6':
			for j := 0; j < 2; j++ {
				off := j * 124
				if get(line, 8+off, 12+off) == "" {
					continue
				}
				s := Segment{Origin: get(line, 8+off, 12+off), Destination: get(line, 13+off, 17+off), Carrier: get(line, 38+off, 40+off),
					Cabin: get(line, 41+off, 41+off), Class: get(line, 42+off, 43+off), NotValidBefore: get(line, 51+off, 55+off), NotValidAfter: get(line, 56+off, 60+off),
					FareBasis: get(line, 61+off, 75+off), Flight: get(line, 76+off, 80+off), Baggage: get(line, 86+off, 88+off), Status: get(line, 89+off, 90+off),
					Coupon: atoi(get(line, 91+off, 91+off)), Stopover: get(line, 92+off, 92+off), Equipment: get(line, 97+off, 99+off)}
				if d := get(line, 44+off, 50+off); len(d) == 7 {
					if t, err := time.Parse("02Jan06", d[:2]+strings.ToUpper(d[2:3])+strings.ToLower(d[3:5])+d[5:]); err == nil {
						s.Departs = t
						if hm := get(line, 81+off, 85+off); len(hm) == 4 {
							if tt, err := time.Parse("1504", hm); err == nil {
								s.Departs = t.Add(time.Duration(tt.Hour())*time.Hour + time.Duration(tt.Minute())*time.Minute)
								s.DepartsHasTime = true
							}
						}
					}
				}
				tx.Segments = append(tx.Segments, s)
			}
		case '7':
			tx.FareText, tx.TotalText = get(line, 8, 19), get(line, 32, 43)
			tx.OriginalDocument, tx.OriginalLocation, tx.OriginalAgent = get(line, 222, 235), get(line, 236, 238), get(line, 246, 253)
			if d := get(line, 239, 245); len(d) == 7 {
				if t, err := time.Parse("02Jan06", d[:2]+strings.ToUpper(d[2:3])+strings.ToLower(d[3:5])+d[5:]); err == nil {
					tx.OriginalIssued = t
				}
			}
		case '8':
			tx.Payments = append(tx.Payments, Payment{Account: get(line, 8, 26), Amount: atoi64(get(line, 27, 37)), Approval: get(line, 38, 43), Type: get(line, 50, 59), Expiry: get(line, 60, 63)})
		case '9':
		default:
			f.Fragments = append(f.Fragments, strings.TrimRight(line, " "))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("bsp: read RET: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("bsp: empty file")
	}
	for _, num := range order {
		tx := byNum[num]
		// The fare is the document amount less its taxes, which the RET
		// does not carry as a field of its own.
		var taxes int64
		for _, t := range tx.Taxes {
			taxes += t.Amount
		}
		tx.Fare = tx.Total - taxes
		f.Transactions = append(f.Transactions, *tx)
	}
	return f, nil
}

func retBlank(id string) []byte {
	rec := []byte(strings.Repeat(" ", RETRecordLen))
	rec[0] = id[0]
	return rec
}

func putRET(rec []byte, start, end int, s string, numeric bool) { put(rec, start, end, s, numeric) }

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
