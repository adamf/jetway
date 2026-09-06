package config

import "testing"

func FuzzParse(f *testing.F) {
	f.Add([]byte(good))
	f.Add([]byte("identity:\n  designator: XX\n  tty_address: LONXXXX\ningress:\n  - name: a\n    kind: tcp\n    addr: :7000\n    framing:\n      kind: sentinel\n      terminator: \"\\nNNNN\\n\"\n"))
	f.Add([]byte("identity: ${HOME}\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if c, err := Parse(raw); err == nil && c != nil {
			_ = c
		}
	})
}
