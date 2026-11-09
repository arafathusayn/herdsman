package context

import (
	"crypto/rand"
	"strings"
	"time"
)

const (
	ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ulidLength   = 26
	ulidTimeLen  = 10
	ulidStepLen  = 12 // digits of random step when an id must follow another
)

// newULID makes an id from the clock and 80 random bits.
func newULID(at time.Time) string {
	buf := make([]byte, ulidLength)
	rand.Read(buf[ulidTimeLen:])
	for i := ulidTimeLen; i < ulidLength; i++ {
		buf[i] = ulidAlphabet[buf[i]&31]
	}
	ms := at.UnixMilli()
	for i := ulidTimeLen - 1; i >= 0; i-- {
		buf[i] = ulidAlphabet[ms&31]
		ms >>= 5
	}
	return string(buf)
}

// nextULID makes an id that sorts after the given one, which may be empty. A
// clock that has not passed it does not move the order back.
func nextULID(after string, now time.Time) string {
	if after == "" || now.UnixMilli() > ulidTime(after).UnixMilli() {
		return newULID(now)
	}
	return advance(after)
}

// advance makes an id above the given one by a random step of up to 60 bits, so
// that clones starting from the same id choose different ones. The step carries
// from the random part into the time.
func advance(id string) string {
	buf := []byte(id)
	noise := make([]byte, ulidStepLen)
	rand.Read(noise)
	carry := 1
	for i := len(buf) - 1; i >= 0; i-- {
		digit := strings.IndexByte(ulidAlphabet, buf[i]) + carry
		if at := len(buf) - 1 - i; at < ulidStepLen {
			digit += int(noise[at] & 31)
		}
		buf[i] = ulidAlphabet[digit&31]
		carry = digit >> 5
	}
	return string(buf)
}

// validULID accepts the canonical upper-case form only, so that one event has
// exactly one file name.
func validULID(id string) bool {
	if len(id) != ulidLength || id[0] > '7' {
		return false
	}
	for i := range len(id) {
		if strings.IndexByte(ulidAlphabet, id[i]) < 0 {
			return false
		}
	}
	return true
}

// ulidTime decodes the clock part of a valid id.
func ulidTime(id string) time.Time {
	var ms int64
	for i := range ulidTimeLen {
		ms = ms<<5 | int64(strings.IndexByte(ulidAlphabet, id[i]))
	}
	return time.UnixMilli(ms).UTC()
}
