package toml

import (
	"math"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// digitsOK checks a run of digits with underscores only between digits.
func digitsOK(s string, valid func(byte) bool) bool {
	if s == "" || s[0] == '_' || s[len(s)-1] == '_' || strings.Contains(s, "__") {
		return false
	}
	for i := range len(s) {
		if s[i] != '_' && !valid(s[i]) {
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
func isOctal(c byte) bool  { return c >= '0' && c <= '7' }
func isBinary(c byte) bool { return c == '0' || c == '1' }

func noUnderscores(s string) string { return strings.ReplaceAll(s, "_", "") }

// parseNumber classifies a token as an integer or float. A non-empty message
// says why it is neither.
func parseNumber(token string) (*tree.Node, string) {
	switch token {
	case "inf", "+inf":
		return tree.NewFloat(math.Inf(1)), ""
	case "-inf":
		return tree.NewFloat(math.Inf(-1)), ""
	case "nan", "+nan", "-nan":
		return tree.NewFloat(math.NaN()), ""
	}
	if len(token) > 2 && token[0] == '0' {
		switch token[1] {
		case 'x':
			return parsePrefixed(token[2:], 16, isHex)
		case 'o':
			return parsePrefixed(token[2:], 8, isOctal)
		case 'b':
			return parsePrefixed(token[2:], 2, isBinary)
		}
	}
	unsigned := strings.TrimLeft(token, "+-")
	if len(token)-len(unsigned) > 1 {
		return nil, "invalid number"
	}
	if strings.ContainsAny(unsigned, ".eE") {
		return parseFloat(token, unsigned)
	}
	if !digitsOK(unsigned, isDigit) {
		return nil, "invalid value"
	}
	if len(unsigned) > 1 && unsigned[0] == '0' {
		return nil, "leading zero in an integer"
	}
	v, err := strconv.ParseInt(noUnderscores(token), 10, 64)
	if err != nil {
		return nil, "integer out of range"
	}
	return tree.NewInt(v), ""
}

func parsePrefixed(digits string, base int, valid func(byte) bool) (*tree.Node, string) {
	if !digitsOK(digits, valid) {
		return nil, "invalid digits in an integer"
	}
	v, err := strconv.ParseInt(noUnderscores(digits), base, 64)
	if err != nil {
		return nil, "integer out of range"
	}
	return tree.NewInt(v), ""
}

func parseFloat(token, unsigned string) (*tree.Node, string) {
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(unsigned), "e")
	whole, fraction, hasFraction := strings.Cut(mantissa, ".")
	if !digitsOK(whole, isDigit) || (hasFraction && !digitsOK(fraction, isDigit)) {
		return nil, "invalid float"
	}
	if len(whole) > 1 && whole[0] == '0' {
		return nil, "leading zero in a float"
	}
	if hasExponent && !digitsOK(strings.TrimLeft(exponent, "+-"), isDigit) {
		return nil, "invalid exponent"
	}
	v, err := strconv.ParseFloat(noUnderscores(token), 64)
	if err != nil {
		return nil, "float out of range or malformed"
	}
	return tree.NewFloat(v), ""
}

// twoDigits reads two digits at s[i:], or -1 when they are not there.
func twoDigits(s string, i int) int {
	if len(s) < i+2 || !isDigit(s[i]) || !isDigit(s[i+1]) {
		return -1
	}
	return int(s[i]-'0')*10 + int(s[i+1]-'0')
}

func daysIn(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// parseDatetime classifies a token that has the shape of a date or time. It
// returns no node and no message for a token of any other shape, and a
// message for one that has the shape but is not valid.
func parseDatetime(token string) (*tree.Node, string) {
	isDate := len(token) >= 5 && token[4] == '-' && digitsOK(token[:4], isDigit) && !strings.Contains(token[:4], "_")
	isTime := len(token) >= 3 && token[2] == ':'
	if !isDate && !isTime {
		return nil, ""
	}
	if isTime {
		if msg, rest := parseTime(token); msg != "" || rest != "" {
			return nil, "invalid time"
		}
		return tree.NewDatetime(tree.LocalTime, token), ""
	}
	if !validDate(token) {
		return nil, "invalid date"
	}
	if len(token) == 10 {
		return tree.NewDatetime(tree.LocalDate, token), ""
	}
	if c := token[10]; c != 'T' && c != 't' && c != ' ' {
		return nil, "invalid date-time separator"
	}
	msg, rest := parseTime(token[11:])
	if msg != "" {
		return nil, msg
	}
	if rest == "" {
		return tree.NewDatetime(tree.LocalDateTime, token), ""
	}
	if !validOffset(rest) {
		return nil, "invalid time offset"
	}
	return tree.NewDatetime(tree.OffsetDateTime, token), ""
}

func validDate(s string) bool {
	if len(s) < 10 || s[7] != '-' {
		return false
	}
	year, month, day := twoDigits(s, 0)*100+twoDigits(s, 2), twoDigits(s, 5), twoDigits(s, 8)
	return month >= 1 && month <= 12 && day >= 1 && day <= daysIn(year, month)
}

// parseTime reads hh:mm:ss[.fraction] and returns what follows it.
func parseTime(s string) (msg, rest string) {
	if len(s) < 8 || s[2] != ':' || s[5] != ':' {
		return "invalid time", ""
	}
	hour, minute, second := twoDigits(s, 0), twoDigits(s, 3), twoDigits(s, 6)
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 60 {
		return "invalid time", ""
	}
	rest = s[8:]
	if strings.HasPrefix(rest, ".") {
		n := 1
		for n < len(rest) && isDigit(rest[n]) {
			n++
		}
		if n == 1 {
			return "invalid fractional seconds", ""
		}
		rest = rest[n:]
	}
	return "", rest
}

func validOffset(s string) bool {
	if s == "Z" || s == "z" {
		return true
	}
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return false
	}
	hour, minute := twoDigits(s, 1), twoDigits(s, 4)
	return hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59
}
