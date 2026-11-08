package channels

import (
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

// maxReplyURLBytes is the channels_reply_url_http limit, said here first so a
// longer one is a 400 rather than a failed insert.
const maxReplyURLBytes = 2048

// checkReplyURL refuses a reply URL the brain should not post to. A literal
// private address is refused here; a name that resolves to one is refused when
// the sender dials it, which is the check that counts.
//
// No query, no credentials: every member can read the URL in the channel
// list, so a token in it would be a token everyone holds. The receiver checks
// the signature instead.
func checkReplyURL(raw string, allow func(netip.Addr) bool) error {
	u, err := url.Parse(raw)
	switch {
	case len(raw) > maxReplyURLBytes:
		return errors.New("reply_url must be at most 2048 bytes")
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "":
		return errors.New("reply_url must be an http or https URL with a host")
	case u.User != nil:
		return errors.New("reply_url must not carry credentials")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return errors.New("reply_url must not have a query or a fragment; the signature is what authenticates a reply")
	}

	if addr, err := netip.ParseAddr(u.Hostname()); err == nil {
		addr = addr.Unmap()
		if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || !allow(addr) {
			return errors.New("reply_url must not point at a private, loopback or link-local address")
		}
	}
	return nil
}

// nullableString tells a field sent as null from one not sent at all.
type nullableString struct {
	Set   bool
	Value *string
}

func (n *nullableString) UnmarshalJSON(b []byte) error {
	n.Set = true
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	s = strings.TrimSpace(s)
	n.Value = &s
	return nil
}
