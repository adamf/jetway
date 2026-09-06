package ssim

import (
	"bytes"
	"testing"
)

// FuzzParseMessage covers the SSM/ASM teletype grammar, which arrives over a
// Type B link.
func FuzzParseMessage(f *testing.F) {
	f.Add("ASM\nUTC\nCNL\nBA0117/16DEC\nLHR JFK")
	f.Add("SSM\nUTC\nNEW\nBA0117\n01JUL-15AUG 135\nLHR 0900 JFK 1200 744")
	f.Add("ASM\nLT\nTIM\nLH0400/12JAN\nFRA 0800 LHR 0830")
	f.Add("ASM\nCNL\nLH0400/12JAN")
	f.Add("SSM\nUTC\nRIN\nBA0117\n01JUL")
	f.Add("ASM\nUTC\nADM\nBA0117/16DEC\nSI SOMETHING A CARRIER INVENTED")
	f.Add("SSM\nUTC\nNEW\nBA0117/1\n01JUL25-15AUG25 1234567\nLHR 0900 JFK 1200 744 J C Y M\nEQT 744\n")
	f.Fuzz(func(t *testing.T, text string) {
		_ = IsSchedule(text)
		m, err := Parse(text)
		if err != nil {
			return
		}
		out := m.Build()
		_, _ = Parse(out)
	})
}

// FuzzParseFile covers the fixed-width SSIM chapter 7 file reader, which is
// fed from an operator's schedule file.
func FuzzParseFile(f *testing.F) {
	var buf bytes.Buffer
	if err := sampleFile().Write(&buf); err == nil {
		f.Add(buf.Bytes())
	}
	f.Add([]byte(sampleType3 + "\n" + sampleType4 + "\n"))
	f.Add([]byte("1AIRLINE STANDARD SCHEDULE DATA SET\n2UXX  0008    01JAN2501DEC25\n" + sampleType3 + "\n5 XX  \n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		file, err := ParseFile(bytes.NewReader(raw))
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err := file.Write(&out); err == nil {
			_, _ = ParseFile(bytes.NewReader(out.Bytes()))
		}
		for _, l := range file.Legs {
			_ = file.OperatingFlight(l)
		}
	})
}
