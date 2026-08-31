// Package ulid implements lexicographically sortable, time-ordered identifiers.
//
// Message identifiers in a gateway are used as database keys, as log
// correlation ids and as the sort order of an audit trail. A ULID gives all
// three: the first 48 bits are a millisecond timestamp, so string ordering is
// time ordering, and the remaining 80 bits are random, so ids are unguessable
// and safe to expose.
//
// Implemented here rather than pulled in as a dependency because it is forty
// lines and this project's dependency tree is something carriers will audit.
package ulid

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"
)

// encoding is Crockford base32: no I, L, O or U, so ids survive being read
// aloud and transcribed.
const encoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Len is the encoded length: 10 characters of timestamp, 16 of randomness.
const Len = 26

var (
	mu       sync.Mutex
	lastMS   uint64
	lastRand [10]byte
)

// New returns a new ULID for the current time.
func New() string { return NewAt(time.Now()) }

// NewAt returns a new ULID stamped with t.
//
// Within a single millisecond the random component is incremented rather than
// redrawn, so ids minted in a tight loop stay strictly ordered. Without that,
// two messages received in the same millisecond can sort in the wrong order,
// and an audit trail that reorders is worse than no audit trail.
func NewAt(t time.Time) string {
	ms := uint64(t.UnixMilli())

	mu.Lock()
	if ms == lastMS {
		incr(&lastRand)
	} else {
		lastMS = ms
		if _, err := rand.Read(lastRand[:]); err != nil {
			// The kernel CSPRNG does not fail in practice. If it ever does,
			// a degraded id is better than a panic in the ingest path, and the
			// timestamp still orders correctly.
			binary.BigEndian.PutUint64(lastRand[0:8], ms*2654435761)
		}
	}
	r := lastRand
	mu.Unlock()

	var b [16]byte
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	copy(b[6:], r[:])
	return encode(b)
}

func incr(b *[10]byte) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return
		}
	}
}

func encode(b [16]byte) string {
	var out [Len]byte
	// 128 bits into 26 base32 characters: the first character carries only the
	// top 3 bits.
	out[0] = encoding[(b[0]&224)>>5]
	out[1] = encoding[b[0]&31]
	idx := 2
	var acc, bits uint32
	for i := 1; i < 16; i++ {
		acc = acc<<8 | uint32(b[i])
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[idx] = encoding[(acc>>bits)&31]
			idx++
		}
	}
	return string(out[:])
}

// ErrInvalid is returned when a string is not a well-formed ULID.
var ErrInvalid = errors.New("ulid: malformed identifier")

// Time extracts the timestamp encoded in a ULID.
func Time(s string) (time.Time, error) {
	if len(s) != Len {
		return time.Time{}, ErrInvalid
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		v := strings.IndexByte(encoding, s[i])
		if v < 0 {
			return time.Time{}, ErrInvalid
		}
		ms = ms<<5 | uint64(v)
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}

// Valid reports whether s is a well-formed ULID.
func Valid(s string) bool {
	if len(s) != Len {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(encoding, s[i]) < 0 {
			return false
		}
	}
	return true
}
