package websearch

import (
	"net/netip"
	"testing"
)

// The dialer is handed IPv4 addresses for a mapped URL, so this is checked
// here: an address that arrives mapped, or with a zone, is judged as the
// address it is.
func TestAMappedOrZonedAddressIsJudgedAsTheAddressItIs(t *testing.T) {
	everything := func(netip.Addr) bool { return true }
	for _, a := range []string{"::ffff:169.254.169.254", "fe80::1%en0"} {
		if dialable(netip.MustParseAddr(a), everything) {
			t.Errorf("%s is dialable, want link-local refused", a)
		}
	}

	var judged netip.Addr
	dialable(netip.MustParseAddr("::ffff:203.0.113.7"), func(a netip.Addr) bool { judged = a; return true })
	if judged != netip.MustParseAddr("203.0.113.7") {
		t.Errorf("allow judged %s, want the IPv4 address it maps", judged)
	}
}
