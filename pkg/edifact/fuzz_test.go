package edifact

import "testing"

// FuzzParseInterchange exercises the syntax layer under every option the
// gateway uses, plus the CONTRL reader and builders that consume the result.
func FuzzParseInterchange(f *testing.F) {
	seeds := []string{
		samplePAORES,
		"UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+A?+B'UNT+3+1'UNZ+1+1'",
		"UNA:+.? 'UNB+UNOA:4+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+A*B'UNT+3+1'UNZ+1+1'",
		"UNA:+.? '\nUNB+UNOA:3+A+B+250101:0000+1'\nUNH+1+PAORES:96:1:IA'\nUNT+2+1'\nUNZ+1+1'\n",
		"UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+SMITH?+SON:A??B:C?:D?'E'UNT+3+1'UNZ+1+1'",
		"UNB+UNOA:3+A+B+250101:0000+1'UNG+G+A+B+250101:0000+1+UN+1:1'UNH+1+X:1:1:IA'UNT+2+1'UNE+1+1'UNZ+1+1'",
		"UNB+UNOA:3+AA:ZZ+1J:ZZ+260829:1200+IC0001'UNH+1+CONTRL:D:3:UN'UCI+IC0001+AA:ZZ+1J:ZZ+7'UNT+3+1'UNZ+1+IC0001'",
		"BANNER LINE\r\nUNB+UNOA:4+A+B+250101:0000+1'UNH+1+X:1:1:IA'UNT+2+1'UNZ+1+1'",
		"UNB+UNOA:9+A+B+250101:0000+1'UNH+1+X:1:1:IA'UNT+2+1'UNZ+1+1'",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, opts := range []ParseOptions{
			{},
			{SkipPreamble: true},
			{Strict: true},
			{SkipCharsetCheck: true, Scan: ScanOptions{PreserveLineBreaks: true}},
			{Scan: ScanOptions{MaxSegments: 8}},
		} {
			ic, err := Parse(raw, opts)
			if err != nil || ic == nil {
				continue
			}
			_ = ic.HasErrors()
			_ = ic.Sender()
			_ = ic.Recipient()
			_ = ic.ControlRef()
			_, _ = ic.PreparedDate()
			_ = ic.AckRequested()
			_ = ic.TestIndicator()
			_, _ = ic.SyntaxIdentifier()
			_ = ic.Syntax.UNAString()
			_ = ic.Syntax.IsDefault()
			for _, m := range ic.Messages {
				_ = m.ID().String()
				_ = m.Reference()
				_ = m.Find("TVL")
				_, _ = m.First("TVL")
				if IsCONTRL(m) {
					_, _ = ParseCONTRL(m)
				}
			}
			_ = Check(ic)
			_ = Receipt(ic)
			for _, d := range ic.Diagnostics {
				_ = d.String()
			}
			for _, s := range ic.Segments {
				_ = s.String()
			}
			if out, err := ic.Encode(EncodeOptions{}); err == nil {
				_, _ = Parse(out, opts)
			}
		}
		_, _, _, _ = Scan(raw, ScanOptions{})
	})
}
