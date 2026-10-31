package webfetch

import (
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

const (
	timeout      = 15 * time.Second
	maxRedirects = 5
)

func NewClient(allow func(netip.Addr) bool) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: websearch.Transport(allow, "web fetch"),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("the address redirected more than %d times", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("a redirect went to %s, which web fetch does not follow", req.URL.Scheme)
			}
			return nil
		},
	}
}
