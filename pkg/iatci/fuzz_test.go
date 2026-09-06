package iatci

import (
	"testing"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
)

func FuzzParseCheckIn(f *testing.F) {
	f.Add([]byte(handWritten))
	if ic, err := BuildDCQCKI(sampleRequest(), opts); err == nil {
		if out, err := ic.Encode(edifact.EncodeOptions{}); err == nil {
			f.Add(out)
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		ic, err := edifact.Parse(raw, edifact.ParseOptions{})
		if err != nil {
			return
		}
		for _, m := range ic.Messages {
			_ = IsCheckIn(m)
			_ = IsCheckInResponse(m)
			if req, err := ParseDCQCKI(m); err == nil {
				if out, err := BuildDCQCKI(req, padis.BuildOptions{}); err == nil {
					_, _ = out.Encode(edifact.EncodeOptions{})
				}
			}
			if res, err := ParseDCRCKA(m); err == nil {
				if out, err := BuildDCRCKA(res, padis.BuildOptions{}); err == nil {
					_, _ = out.Encode(edifact.EncodeOptions{})
				}
			}
		}
	})
}
