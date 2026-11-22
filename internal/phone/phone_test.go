package phone

import "testing"

func TestSplit(t *testing.T) {
	cases := map[string][2]string{
		"+919876543210":   {"+91", "9876543210"},
		"+91 98765 43210": {"+91", "9876543210"},
		"+14155550100":    {"+1", "4155550100"},
		"+12685551234":    {"+1268", "5551234"},
	}
	for value, want := range cases {
		code, national, err := Split(value)
		if err != nil || code != want[0] || national != want[1] {
			t.Errorf("Split(%q) = %q, %q, %v; want %q, %q", value, code, national, err, want[0], want[1])
		}
	}
}

func TestSplitNeedsCountryCode(t *testing.T) {
	if _, _, err := Split("9876543210"); err == nil {
		t.Fatal("a number without + should be rejected")
	}
}
