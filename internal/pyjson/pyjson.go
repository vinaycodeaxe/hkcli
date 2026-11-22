// Package pyjson reads and writes JSON the way Python's json module does.
//
// hkcli started as a Python tool, and its stores, packets and --json output must
// stay compatible: objects keep their key order, integers and floats stay
// distinct, and encoding follows json.dumps (separators, escaping and float
// formatting). Str, Repr and Truthy mirror Python's str(), repr() and bool().
package pyjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Member is one key/value pair of an Object.
type Member struct {
	Key   string
	Value any
}

// Object is a JSON object that keeps its key order, like a Python dict.
type Object []Member

// O builds an Object from alternating keys and values.
func O(kv ...any) Object {
	if len(kv)%2 != 0 {
		panic("pyjson.O: odd number of arguments")
	}
	o := make(Object, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Lookup returns the value stored under key and whether the key is present.
func (o Object) Lookup(key string) (any, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// Get returns the value stored under key, or nil (Python's dict.get).
func (o Object) Get(key string) any {
	v, _ := o.Lookup(key)
	return v
}

// Obj returns the value under key if it is an object, else nil.
func (o Object) Obj(key string) Object {
	v, _ := o.Get(key).(Object)
	return v
}

// Set stores value under key, keeping the position of an existing key.
func (o *Object) Set(key string, value any) {
	for i := range *o {
		if (*o)[i].Key == key {
			(*o)[i].Value = value
			return
		}
	}
	*o = append(*o, Member{key, value})
}

// Loads parses JSON like json.loads: objects become Object, arrays []any,
// integers int64 (json.Number when they do not fit), other numbers float64.
func Loads(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("extra data after the JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			obj := Object{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return nil, err
				}
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Set(key.(string), val)
			}
			_, err := dec.Token()
			return obj, err
		}
		arr := []any{}
		for dec.More() {
			val, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		_, err := dec.Token()
		return arr, err
	case json.Number:
		return number(t), nil
	default:
		return t, nil
	}
}

func number(n json.Number) any {
	s := string(n)
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		return n
	}
	f, _ := strconv.ParseFloat(s, 64) // out of range gives ±Inf, like Python
	return f
}

type style struct {
	item, key, indent string
	ascii             bool
}

// Dumps is json.dumps(v, ensure_ascii=False).
func Dumps(v any) string { return encode(v, style{item: ", ", key: ": "}) }

// DumpsCompact is json.dumps(v, ensure_ascii=False, separators=(",", ":")).
func DumpsCompact(v any) string { return encode(v, style{item: ",", key: ":"}) }

// DumpsIndent is json.dumps(v, indent=2, ensure_ascii=False).
func DumpsIndent(v any) string { return encode(v, style{item: ",", key: ": ", indent: "  "}) }

// DumpsASCII is json.dumps(v) with its defaults, which escape non-ASCII text.
func DumpsASCII(v any) string { return encode(v, style{item: ", ", key: ": ", ascii: true}) }

func encode(v any, s style) string {
	e := &encoder{style: s}
	e.value(v, 0)
	return e.buf.String()
}

type encoder struct {
	style
	buf strings.Builder
}

func (e *encoder) value(v any, depth int) {
	switch x := v.(type) {
	case nil:
		e.buf.WriteString("null")
	case bool:
		if x {
			e.buf.WriteString("true")
		} else {
			e.buf.WriteString("false")
		}
	case string:
		e.str(x)
	case int:
		e.buf.WriteString(strconv.Itoa(x))
	case int64:
		e.buf.WriteString(strconv.FormatInt(x, 10))
	case float64:
		e.buf.WriteString(jsonFloat(x))
	case json.Number:
		e.buf.WriteString(string(x))
	case Object:
		e.container('{', '}', len(x), depth, func(i int) {
			e.str(x[i].Key)
			e.buf.WriteString(e.key)
			e.value(x[i].Value, depth+1)
		})
	case []any:
		e.container('[', ']', len(x), depth, func(i int) { e.value(x[i], depth+1) })
	case []Object:
		e.container('[', ']', len(x), depth, func(i int) { e.value(x[i], depth+1) })
	case []string:
		e.container('[', ']', len(x), depth, func(i int) { e.str(x[i]) })
	default:
		e.str(Str(v)) // like json.dumps(..., default=str)
	}
}

func (e *encoder) container(open, close byte, n, depth int, item func(int)) {
	e.buf.WriteByte(open)
	if n == 0 {
		e.buf.WriteByte(close)
		return
	}
	newline := ""
	if e.indent != "" {
		newline = "\n" + strings.Repeat(e.indent, depth+1)
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			e.buf.WriteString(e.item)
		}
		e.buf.WriteString(newline)
		item(i)
	}
	if e.indent != "" {
		e.buf.WriteString("\n" + strings.Repeat(e.indent, depth))
	}
	e.buf.WriteByte(close)
}

func (e *encoder) str(s string) {
	e.buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			e.buf.WriteString(`\"`)
		case '\\':
			e.buf.WriteString(`\\`)
		case '\n':
			e.buf.WriteString(`\n`)
		case '\r':
			e.buf.WriteString(`\r`)
		case '\t':
			e.buf.WriteString(`\t`)
		case '\b':
			e.buf.WriteString(`\b`)
		case '\f':
			e.buf.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(&e.buf, `\u%04x`, r)
			case e.ascii && r > 0x7e:
				if r > 0xffff {
					r -= 0x10000
					fmt.Fprintf(&e.buf, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
				} else {
					fmt.Fprintf(&e.buf, `\u%04x`, r)
				}
			default:
				e.buf.WriteRune(r)
			}
		}
	}
	e.buf.WriteByte('"')
}

func jsonFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return reprFloat(f)
}

// reprFloat formats f like Python's repr(float): the shortest round-trip digits,
// fixed notation for exponents -4..15 with at least one decimal, else 1e+16 style.
func reprFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	mantissa, expText, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expText)
	digits := strings.Replace(mantissa, ".", "", 1)
	point := exp + 1
	switch {
	case point <= -4 || point > 16:
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		expSign := "+"
		if exp < 0 {
			expSign, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, m, expSign, exp)
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits)) + ".0"
	default:
		return sign + digits[:point] + "." + digits[point:]
	}
}

// IntString formats an integral float like Python's str(int(f)).
func IntString(f float64) string {
	if math.Abs(f) < 1<<62 {
		return strconv.FormatInt(int64(f), 10)
	}
	i, _ := big.NewFloat(f).Int(nil)
	return i.String()
}

// Str formats v like Python's str().
func Str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return reprFloat(x)
	case json.Number:
		return string(x)
	case Object, []any:
		return Repr(v)
	default:
		return fmt.Sprint(v)
	}
}

// Repr formats v like Python's repr().
func Repr(v any) string {
	switch x := v.(type) {
	case string:
		return reprString(x)
	case Object:
		parts := make([]string, len(x))
		for i, m := range x {
			parts[i] = reprString(m.Key) + ": " + Repr(m.Value)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = Repr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return Str(v)
	}
}

func reprString(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == quote || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < utf8.RuneSelf || unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// Truthy reports whether Python's bool() would be true for v.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case json.Number:
		return true // only integers too large for int64 are kept as Number
	case Object:
		return len(x) > 0
	case []any:
		return len(x) > 0
	default:
		return true
	}
}

// Or returns a if it is truthy, else b (Python's `a or b`).
func Or(a, b any) any {
	if Truthy(a) {
		return a
	}
	return b
}

// IsSpace reports whether Python's str.isspace() is true for r.
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// Strip removes leading and trailing whitespace like Python's str.strip().
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }
