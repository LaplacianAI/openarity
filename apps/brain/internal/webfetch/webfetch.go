package webfetch

import (
	"context"
	"net/netip"
	"strings"
)

type Page struct {
	URL      string
	Title    string
	Markdown string
	Cut      bool
}

type Fetcher interface {
	Fetch(ctx context.Context, url string) (Page, error)
}

// NumericHost is a host a browser reads as an IPv4 address, because its last
// label is a number, that is not one as written: 2130706433, 0x7f.1, 127.1 or
// 127.0.0.01. Resolvers and scrapers disagree on what such a host names, so
// it is refused rather than judged.
func NumericHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	last := strings.ToLower(labels[len(labels)-1])
	if hex, ok := strings.CutPrefix(last, "0x"); ok {
		return strings.Trim(hex, "0123456789abcdef") == ""
	}
	return last != "" && strings.Trim(last, "0123456789") == ""
}
