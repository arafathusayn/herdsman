// Package ws implements RFC 6455 WebSocket framing for servers and clients,
// without extensions. It depends only on the standard library so that the SDK
// needs no package from the root module.
package ws

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Opcode is a WebSocket frame opcode.
type Opcode byte

// The opcodes RFC 6455 defines.
const (
	OpContinuation Opcode = 0x0
	OpText         Opcode = 0x1
	OpBinary       Opcode = 0x2
	OpClose        Opcode = 0x8
	OpPing         Opcode = 0x9
	OpPong         Opcode = 0xA
)

// Close codes this package sends or reports.
const (
	CloseNormal         uint16 = 1000
	CloseGoingAway      uint16 = 1001
	CloseProtocolError  uint16 = 1002
	CloseNoStatus       uint16 = 1005
	CloseInvalidPayload uint16 = 1007
	CloseMessageTooBig  uint16 = 1009
)

const (
	maxControlPayload = 125
	maxCloseReason    = maxControlPayload - 2
)

// errShortHeader means the buffer does not yet hold the whole frame header.
var errShortHeader = errors.New("ws: short frame header")

// ProtocolError is a violation of RFC 6455 detected on this side. The
// connection is failed with Code.
type ProtocolError struct {
	Code   uint16
	Reason string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("ws: protocol error %d: %s", e.Code, e.Reason)
}

// CloseError reports the close frame the peer sent. Code is CloseNoStatus when
// the frame carried no code.
type CloseError struct {
	Code   uint16
	Reason string
}

func (e *CloseError) Error() string {
	return fmt.Sprintf("ws: peer closed: %d %s", e.Code, e.Reason)
}

func protocolErr(reason string) *ProtocolError {
	return &ProtocolError{Code: CloseProtocolError, Reason: reason}
}

func (o Opcode) control() bool { return o&0x8 != 0 }

func (o Opcode) known() bool {
	switch o {
	case OpContinuation, OpText, OpBinary, OpClose, OpPing, OpPong:
		return true
	}
	return false
}

type header struct {
	fin    bool
	op     Opcode
	masked bool
	length uint64
	key    [4]byte
}

// headerSize returns the full header size given the frame's second byte.
func headerSize(second byte) int {
	size := 2
	switch second & 0x7F {
	case 126:
		size += 2
	case 127:
		size += 8
	}
	if second&0x80 != 0 {
		size += 4
	}
	return size
}

// parseHeader decodes one frame header from b. Every rule that needs only the
// first two bytes is applied before asking for more, so a bad frame fails
// without waiting on bytes the peer may never send.
func parseHeader(b []byte) (header, int, error) {
	if len(b) < 2 {
		return header{}, 0, errShortHeader
	}
	h := header{
		fin:    b[0]&0x80 != 0,
		op:     Opcode(b[0] & 0x0F),
		masked: b[1]&0x80 != 0,
	}
	if b[0]&0x70 != 0 {
		return header{}, 0, protocolErr("reserved bits set")
	}
	if !h.op.known() {
		return header{}, 0, protocolErr("reserved opcode")
	}
	short := b[1] & 0x7F
	if h.op.control() && (!h.fin || short > maxControlPayload) {
		return header{}, 0, protocolErr("invalid control frame")
	}
	size := headerSize(b[1])
	if len(b) < size {
		return header{}, 0, errShortHeader
	}
	pos := 2
	switch short {
	case 126:
		h.length = uint64(binary.BigEndian.Uint16(b[pos:]))
		pos += 2
		if h.length < 126 {
			return header{}, 0, protocolErr("length not minimally encoded")
		}
	case 127:
		h.length = binary.BigEndian.Uint64(b[pos:])
		pos += 8
		if h.length>>63 != 0 {
			return header{}, 0, protocolErr("length top bit set")
		}
		if h.length <= 0xFFFF {
			return header{}, 0, protocolErr("length not minimally encoded")
		}
	default:
		h.length = uint64(short)
	}
	if h.masked {
		copy(h.key[:], b[pos:])
	}
	return h, size, nil
}

// appendHeader appends the header of one frame to dst.
func appendHeader(dst []byte, fin bool, op Opcode, masked bool, key [4]byte, length uint64) []byte {
	first := byte(op)
	if fin {
		first |= 0x80
	}
	var maskBit byte
	if masked {
		maskBit = 0x80
	}
	dst = append(dst, first)
	switch {
	case length <= maxControlPayload:
		dst = append(dst, maskBit|byte(length))
	case length <= 0xFFFF:
		dst = append(dst, maskBit|126)
		dst = binary.BigEndian.AppendUint16(dst, uint16(length))
	default:
		dst = append(dst, maskBit|127)
		dst = binary.BigEndian.AppendUint64(dst, length)
	}
	if masked {
		dst = append(dst, key[:]...)
	}
	return dst
}

// mask XORs b with key starting at key offset pos and returns the offset for
// the next chunk.
func mask(b []byte, key [4]byte, pos int) int {
	for i := range b {
		b[i] ^= key[(pos+i)&3]
	}
	return (pos + len(b)) & 3
}

// utf8Validator checks UTF-8 across fragment boundaries.
type utf8Validator struct {
	need   int
	lo, hi byte
}

func (v *utf8Validator) feed(b []byte) bool {
	for _, c := range b {
		if v.need > 0 {
			if c < v.lo || c > v.hi {
				return false
			}
			v.need--
			v.lo, v.hi = 0x80, 0xBF
			continue
		}
		v.lo, v.hi = 0x80, 0xBF
		switch {
		case c < 0x80:
		case c >= 0xC2 && c <= 0xDF:
			v.need = 1
		case c >= 0xE0 && c <= 0xEF:
			v.need = 2
			if c == 0xE0 {
				v.lo = 0xA0
			}
			if c == 0xED {
				v.hi = 0x9F
			}
		case c >= 0xF0 && c <= 0xF4:
			v.need = 3
			if c == 0xF0 {
				v.lo = 0x90
			}
			if c == 0xF4 {
				v.hi = 0x8F
			}
		default:
			return false
		}
	}
	return true
}

func (v *utf8Validator) complete() bool { return v.need == 0 }

// validCloseCode reports whether code may appear in a close frame.
func validCloseCode(code uint16) bool {
	switch {
	case code >= 1000 && code <= 1003:
		return true
	case code >= 1007 && code <= 1014:
		return true
	case code >= 3000 && code <= 4999:
		return true
	}
	return false
}
