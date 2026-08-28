package pnr

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// LocatorAlphabet is the character set used for record locators.
//
// I, O, 0 and 1 are omitted. Record locators are read aloud over the phone and
// typed from handwriting, and those four are the pairs people confuse. Thirty-
// two characters also makes the code space an exact power of two, which is what
// lets the permutation below be collision-free rather than merely unlikely to
// collide.
const LocatorAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// LocatorLength is the conventional six characters.
const LocatorLength = 6

// LocatorSpace is the number of distinct locators: 32^6 == 2^30.
const LocatorSpace = 1 << 30

// LocatorAllocator turns a monotonic counter into a record locator.
//
// The obvious implementations are both wrong. Assigning locators sequentially
// leaks booking volume to anyone who makes two bookings, and invites enumeration
// of other people's records. Assigning them randomly requires a uniqueness
// check and a retry loop, which becomes a hot contended row exactly when
// traffic is heaviest.
//
// A keyed Feistel network avoids both. It is a bijection on the code space, so
// distinct counter values always produce distinct locators with no lookup and
// no retry, while the output order reveals nothing about the input order
// without the key.
type LocatorAllocator struct {
	key [32]byte
}

// NewLocatorAllocator builds an allocator from a secret.
//
// The secret must be stable for the life of the deployment and must be treated
// as one: changing it remaps every future locator onto the space in a different
// order, which will eventually collide with locators already issued.
func NewLocatorAllocator(secret []byte) *LocatorAllocator {
	a := &LocatorAllocator{}
	sum := sha256.Sum256(secret)
	a.key = sum
	return a
}

// feistelRounds is enough for the unlinkability we need here. This is an
// obfuscation of ordering, not a cipher protecting a secret, and it is not a
// substitute for authorisation checks on record access.
const feistelRounds = 6

// round is the Feistel round function: a keyed hash truncated to 15 bits.
func (a *LocatorAllocator) round(r uint32, i int) uint32 {
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[0:4], r)
	binary.BigEndian.PutUint32(buf[4:8], uint32(i))
	h := hmac.New(sha256.New, a.key[:])
	h.Write(buf[:])
	return binary.BigEndian.Uint32(h.Sum(nil)[:4]) & 0x7FFF
}

// permute applies the Feistel network to a 30-bit value.
func (a *LocatorAllocator) permute(n uint32) uint32 {
	l, r := (n>>15)&0x7FFF, n&0x7FFF
	for i := 0; i < feistelRounds; i++ {
		l, r = r, l^a.round(r, i)
	}
	return (l << 15) | r
}

// Allocate maps a counter value to a record locator. Counter values must be
// distinct; the database sequence backing them is the source of uniqueness.
func (a *LocatorAllocator) Allocate(counter uint64) string {
	n := a.permute(uint32(counter % LocatorSpace))
	var out [LocatorLength]byte
	for i := LocatorLength - 1; i >= 0; i-- {
		out[i] = LocatorAlphabet[n&31]
		n >>= 5
	}
	return string(out[:])
}

// ValidLocator reports whether s is well formed. It says nothing about whether
// the record exists.
func ValidLocator(s string) bool {
	if len(s) != LocatorLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(LocatorAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// NormaliseLocator canonicalises user input: it uppercases, and strips spaces
// and hyphens that people insert when writing a locator down.
//
// It deliberately does not "correct" characters outside the alphabet. Mapping a
// typed 0 to O, or 1 to I, looks helpful and is not: those characters are
// absent from the alphabet precisely so that no such ambiguity exists, so any
// substitution rule would silently resolve a typo to a real locator belonging
// to somebody else. An unresolvable input must fail as an input error and be
// re-keyed.
func NormaliseLocator(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "-", "", ".", "").Replace(s)
	if !ValidLocator(s) {
		return "", fmt.Errorf("pnr: %q is not a well-formed record locator", s)
	}
	return s, nil
}
