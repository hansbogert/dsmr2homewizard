// Package ct003 emulates a Marstek CT003 (HME-3) smart meter on its local UDP protocol, so
// Marstek batteries (Venus, B2500, Jupiter) configured for a CT003 read the grid from any DSMR
// feed. The behavior follows the community description of the CT003 firmware v122 in
// https://github.com/tomquist/AstraMeter/blob/main/docs/ct002-ct003-protocol.md: it relays the
// meter reading and the batteries' own reports, and leaves all steering to the batteries.
package ct003

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	soh = 0x01
	stx = 0x02
	etx = 0x03

	// Type is the CT type a battery names in its requests and the CT puts first in its replies.
	Type = "HME-3"
	// Wildcard is the CT MAC an unbound battery sends; any CT answers it.
	Wildcard = "000000000000"
)

// frame wraps body ("|f1|f2|…") as SOH STX <length> body ETX <checksum>. The length is the
// whole packet's, its own digits included; the checksum is the XOR of SOH through ETX as two
// uppercase hex digits.
func frame(body string) []byte {
	fixed := len(body) + 5 // SOH, STX, ETX and two checksum digits
	n := fixed + 1
	for n != fixed+len(strconv.Itoa(n)) {
		n = fixed + len(strconv.Itoa(n))
	}
	b := make([]byte, 0, n)
	b = append(b, soh, stx)
	b = strconv.AppendInt(b, int64(n), 10)
	b = append(b, body...)
	b = append(b, etx)
	var x byte
	for _, c := range b {
		x ^= c
	}
	return fmt.Appendf(b, "%02X", x)
}

// unframe checks a packet's framing and checksum and returns its fields after the length.
func unframe(b []byte) ([]string, error) {
	if len(b) < 4 || b[0] != soh || b[1] != stx {
		return nil, errors.New("no SOH STX header")
	}
	end := bytes.IndexByte(b, etx)
	if end < 0 {
		return nil, errors.New("no ETX")
	}
	// The CT sends small checksums with a leading space instead of a zero; accept both.
	want, err := strconv.ParseUint(strings.TrimSpace(string(b[end+1:])), 16, 8)
	if err != nil {
		return nil, fmt.Errorf("bad checksum field %q", b[end+1:])
	}
	var x byte
	for _, c := range b[:end+1] {
		x ^= c
	}
	if x != byte(want) {
		return nil, fmt.Errorf("checksum mismatch: got %02X, packet says %02X", x, want)
	}
	parts := strings.Split(string(b[2:end]), "|")
	if _, err := strconv.ParseInt(parts[0], 10, 16); err != nil {
		return nil, fmt.Errorf("bad length %q", parts[0])
	}
	return parts[1:], nil
}

type request struct {
	devType, devMAC string // the battery's
	ctType, ctMAC   string // the CT it asks for
	phase           byte   // 'A'–'C', 'D' (combined) or '0' (unassigned, detecting)
	power           int16  // the battery's own signed power in W
	participate     bool
}

// maxLen are the CT firmware's field widths (%10[^|] %30[^|] %8[^|] %13[^|]); longer or empty
// values make its sscanf fail, so the request is dropped.
var maxLen = [4]int{10, 30, 8, 13}

func parseRequest(b []byte) (request, error) {
	f, err := unframe(b)
	if err != nil {
		return request{}, err
	}
	if len(f) < 6 {
		return request{}, fmt.Errorf("%d fields, want at least 6", len(f))
	}
	for i, max := range maxLen {
		if f[i] == "" || len(f[i]) > max {
			return request{}, fmt.Errorf("field %d %q: want 1 to %d characters", i+1, f[i], max)
		}
	}
	r := request{devType: f[0], devMAC: f[1], ctType: f[2], ctMAC: f[3], phase: '0',
		participate: true}
	switch p := f[4]; {
	case len(p) > 1:
		return request{}, fmt.Errorf("phase %q: want one character", p)
	case p == "A", p == "B", p == "C", p == "D":
		r.phase = p[0]
	}
	w, err := strconv.ParseInt(f[5], 10, 64)
	if err != nil {
		return request{}, fmt.Errorf("power %q: %w", f[5], err)
	}
	r.power = int16(clamp(w, math.MinInt16, math.MaxInt16))
	// Field 7 (UDP protocol v4) is optional and defaults to participating.
	if len(f) > 6 {
		if v, err := strconv.ParseUint(f[6], 10, 8); err == nil {
			r.participate = v != 0
		}
	}
	return r, nil
}

func clamp(v, lo, hi int64) int64 {
	return min(max(v, lo), hi)
}
