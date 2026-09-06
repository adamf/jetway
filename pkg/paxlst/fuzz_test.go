package paxlst

import (
	"testing"

	"github.com/adamf/jetway/pkg/edifact"
)

func FuzzParse(f *testing.F) {
	for _, s := range []string{example51, example52, example53} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		ic, err := edifact.Parse(raw, edifact.ParseOptions{})
		if err != nil {
			return
		}
		for _, m := range ic.Messages {
			_ = IsPAXLST(m)
			pl, err := Parse(m)
			if err != nil {
				continue
			}
			_ = pl.Describe()
			if out, err := Build(pl, BuildOptions{}); err == nil {
				_, _ = out.Encode(edifact.EncodeOptions{})
			}
		}
	})
}

func FuzzParseDOCS(f *testing.F) {
	for _, s := range []string{
		"DOCS HK1 P/GBR/123456789/GBR/12JUL64/M/23OCT27/SMITH/JOHN",
		"DOCS BA HK1/P/GBR/123456789/GBR/12JUL64/M/23OCT27/SMITH/JOHN",
		"DOCS HK1 ////",
		"DOCS",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseDOCS(s)
	})
}
