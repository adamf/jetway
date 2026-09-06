package transport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzReadFrame runs a byte stream through every framer configuration the
// ingress can be given and checks the framer neither panics nor loops.
func FuzzReadFrame(f *testing.F) {
	var buf bytes.Buffer
	_ = DefaultFramer().WriteFrame(&buf, []byte(`{"peer":"BA","role":"carrier","format":"typeb"}`))
	_ = DefaultFramer().WriteFrame(&buf, []byte("QU LHRRMBA\n.LONRM1J 011200\nHELLO\n"))
	f.Add(buf.Bytes())
	f.Add([]byte("QU LHRRMBA\n.LONRM1J 011200\nHELLO\nNNNN\nQU LHRRMBA\n.LONRM1J 011200\nAGAIN\nNNNN\n"))
	f.Add([]byte{0x00, 0x40, 0x00, 0x00})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'x'})
	f.Add([]byte{0x00, 0x02, 'a', 'b', 0x00, 0x00})
	f.Fuzz(func(t *testing.T, raw []byte) {
		framers := []Framer{
			DefaultFramer(),
			LengthPrefix{HeaderBytes: 2},
			LengthPrefix{HeaderBytes: 2, Inclusive: true},
			LengthPrefix{HeaderBytes: 4, Inclusive: true, LittleEndian: true},
			LengthPrefix{HeaderBytes: 4, Max: 16},
			MATIPProfile(),
			TypeBSentinel(),
			Sentinel{Terminator: []byte("\r\n"), Max: 64},
			Sentinel{Terminator: []byte{0x03}},
		}
		for _, fr := range framers {
			_ = fr.Name()
			r := bufio.NewReaderSize(bytes.NewReader(raw), 64<<10)
			for i := 0; i < 1024; i++ {
				payload, err := fr.ReadFrame(r)
				if len(payload) > 0 {
					var h Hello
					_ = json.Unmarshal(payload, &h)
					var out bytes.Buffer
					_ = fr.WriteFrame(&out, payload)
				}
				if err != nil {
					break
				}
			}
		}
	})
}
