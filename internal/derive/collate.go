package derive

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// ByKey orders two keys the way the payload sorts them: as numbers when both are numbers written the way JavaScript
// writes them ("9" before "10"), and as text otherwise ("OPS-1" before "PROJ-10").
func ByKey(a, b string) int {
	x, okx := numericKey(a)
	y, oky := numericKey(b)
	if okx && oky {
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	return textCompare(a, b)
}

// numericKey reports whether s is a number that String(Number(s)) gives back unchanged, as the JS check does. "010",
// "-0", "+1" and "0x1A" are text.
func numericKey(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	if f == 0 && s != "0" {
		return 0, false
	}
	if strconv.FormatFloat(f, 'f', -1, 64) != s {
		return 0, false
	}
	return f, true
}

// textCompare is an approximation of JavaScript's localeCompare: letters are compared without regard to case first,
// and at the first difference of case the lowercase one comes first (as ICU does). It is not ICU: punctuation sorts as
// in ASCII.
func textCompare(a, b string) int {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if c := strings.Compare(la, lb); c != 0 {
		return c
	}
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if ra[i] != rb[i] {
			if unicode.IsLower(ra[i]) {
				return -1
			}
			return 1
		}
	}
	return sign(len(ra) - len(rb))
}

// naturalCompare is localeCompare with numeric: true: runs of digits compare as numbers, the rest as text. It orders
// the search results by key ("9" before "10", "owner/repo#5" after "12").
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := isDigitByte(a[0]), isDigitByte(b[0])
		ca, cb := chunkOf(a, da), chunkOf(b, db)
		if da && db {
			na, nb := strings.TrimLeft(ca, "0"), strings.TrimLeft(cb, "0")
			if len(na) != len(nb) {
				return sign(len(na) - len(nb))
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
		} else if c := textCompare(ca, cb); c != 0 {
			return c
		}
		a, b = a[len(ca):], b[len(cb):]
	}
	return sign(len(a) - len(b))
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// chunkOf is the leading run of s that is all digits (digits true) or all not digits.
func chunkOf(s string, digits bool) string {
	i := 0
	for i < len(s) && isDigitByte(s[i]) == digits {
		i++
	}
	return s[:i]
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
