package webfetch

import "testing"

// A host whose last label is a number is one a browser reads as IPv4, so one
// that is not an address as written is refused rather than judged.
func TestANumberThatIsNotAnAddressIsNumeric(t *testing.T) {
	t.Parallel()

	for host, want := range map[string]bool{
		"2130706433":     true,
		"0x7f.1":         true,
		"127.1":          true,
		"127.0.0.01":     true,
		"0x7F000001":     true,
		"example.com.":   false,
		"127.0.0.1":      false,
		"::1":            false,
		"example.com":    false,
		"host-1.example": false,
		"1example.com":   false,
	} {
		if got := NumericHost(host); got != want {
			t.Errorf("NumericHost(%q) = %v, want %v", host, got, want)
		}
	}
}
