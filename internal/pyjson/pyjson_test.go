package pyjson

import "testing"

func TestDumpsMatchesPython(t *testing.T) {
	v, err := Loads([]byte(`{"b": 1, "a": [1.0, 2.5, 1e16, 0.0001, 1e-05, -0.0, 123456789012345678901], "c": "é\u2028\"\n<"}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		Dumps(v):                 `{"b": 1, "a": [1.0, 2.5, 1e+16, 0.0001, 1e-05, -0.0, 123456789012345678901], "c": "é` + "\u2028" + `\"\n<"}`,
		DumpsCompact(v):          `{"b":1,"a":[1.0,2.5,1e+16,0.0001,1e-05,-0.0,123456789012345678901],"c":"é` + "\u2028" + `\"\n<"}`,
		DumpsASCII(O("t", "🚗é")): `{"t": "\ud83d\ude97\u00e9"}`,
		DumpsIndent(O("p", Object{}, "q", O("r", nil))): "{\n  \"p\": {},\n  \"q\": {\n    \"r\": null\n  }\n}",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	}
}

func TestStrAndReprMatchPython(t *testing.T) {
	cases := map[string]string{
		Str(nil):                              "None",
		Str(true):                             "True",
		Str(1001.0):                           "1001.0",
		Str(O("1001", "👍", "x", "it's")):      `{'1001': '👍', 'x': "it's"}`,
		Repr("tab\there\x00\u00a0"):           `'tab\there\x00\xa0'`,
		Str([]any{int64(1), "a", nil, false}): `[1, 'a', None, False]`,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}
