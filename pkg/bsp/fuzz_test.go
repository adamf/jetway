package bsp

import (
	"bytes"
	"testing"
)

func FuzzParseHOT(f *testing.F) {
	var buf bytes.Buffer
	if err := sampleFile().Write(&buf); err == nil {
		f.Add(buf.Bytes())
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		file, err := Parse(bytes.NewReader(raw))
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err := file.Write(&out); err == nil {
			_, _ = Parse(bytes.NewReader(out.Bytes()))
		}
	})
}

func FuzzParseRET(f *testing.F) {
	var buf bytes.Buffer
	if err := sampleFile().Write(&buf); err == nil {
		f.Add(buf.Bytes())
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = ParseRET(bytes.NewReader(raw))
		_, _ = CheckDigit(string(raw))
	})
}
