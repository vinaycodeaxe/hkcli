// Package phone splits and recognizes phone numbers. Hike's OTP login wants the
// dial code and the national number separately.
package phone

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
)

// Dial codes offered by Hike Web's country picker. Caribbean NANP codes such as
// 1268 are listed separately there, so the longest matching code wins.
var dialCodes = func() map[string]bool {
	codes := map[string]bool{}
	for _, c := range strings.Split("1,7,20,27,30,31,32,33,34,36,39,40,41,43,44,45,46,47,48,49,51,52,53,54,55,56,57,58,"+
		"60,61,62,63,64,65,66,81,82,84,86,90,91,92,93,94,95,98,211,212,213,216,218,220,221,"+
		"222,223,224,225,226,227,228,229,230,231,232,233,234,235,236,237,238,239,240,241,242,"+
		"243,244,245,246,248,249,250,251,252,253,254,255,256,257,258,260,261,262,263,264,265,"+
		"266,267,268,269,291,297,298,299,350,351,352,353,354,355,356,357,358,359,370,371,372,"+
		"373,374,375,376,377,378,380,381,382,383,385,386,387,389,420,421,423,501,502,503,504,"+
		"505,506,507,508,509,590,591,592,593,594,595,596,597,598,599,670,673,674,675,676,677,"+
		"678,679,680,681,685,686,687,688,689,691,692,850,852,853,855,856,880,886,960,961,962,"+
		"963,964,965,966,967,968,970,971,972,973,974,975,976,977,992,993,994,995,996,998,1242,"+
		"1246,1268,1473,1671,1758,1767,1784,1868,1869,1876", ",") {
		codes[c] = true
	}
	return codes
}()

// Digits keeps only the decimal digits of s.
func Digits(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

// LooksLikePhone reports whether value is written like a phone number
// (an optional +, digits, spaces, dots, dashes and parentheses; 7 to 15 digits).
func LooksLikePhone(value string) bool {
	n := len([]rune(Digits(value)))
	if n < 7 || n > 15 {
		return false
	}
	s := pyjson.Strip(value)
	s = strings.TrimPrefix(s, "+")
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && !pyjson.IsSpace(r) && !strings.ContainsRune("().-", r) {
			return false
		}
	}
	return true
}

// NormalizeE164 returns +<digits> for numbers written with a leading +, else "".
func NormalizeE164(value string) string {
	value = pyjson.Strip(value)
	if !strings.HasPrefix(value, "+") {
		return ""
	}
	digits := Digits(value)
	if n := len([]rune(digits)); n < 7 || n > 15 {
		return ""
	}
	return "+" + digits
}

// Split turns "+91 98765 43210" or "+919876543210" into ("+91", "9876543210").
func Split(value string) (code, national string, err error) {
	value = pyjson.Strip(value)
	if !strings.HasPrefix(value, "+") {
		return "", "", errors.New("phone number must start with + and the country code, e.g. +919876543210")
	}
	parts := strings.FieldsFunc(value[1:], pyjson.IsSpace)
	if len(parts) > 1 && dialCodes[parts[0]] {
		if national := Digits(strings.Join(parts[1:], "")); national != "" {
			return "+" + parts[0], national, nil
		}
	}
	digits := []rune(Digits(value))
	for _, size := range []int{4, 3, 2, 1} {
		if len(digits) < size {
			continue
		}
		if code := string(digits[:size]); dialCodes[code] && len(digits)-size >= 4 {
			return "+" + code, string(digits[size:]), nil
		}
	}
	return "", "", fmt.Errorf("unknown country code in %s; write it as '+<code> <number>'", pyjson.Repr(value))
}
