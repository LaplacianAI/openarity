package main

import (
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	"github.com/LaplacianAI/openarity/apps/brain/internal/worker"
)

const replyTimeout = 10 * time.Second

// newReplyJob sends the outbox's replies. A reply URL may reach what web
// search may: public addresses, and the private networks the operator named.
func newReplyJob(cfg *config.Config, logger *slog.Logger, dbStore *store.Store, sec secrets.Store) (worker.Job, error) {
	reg, err := newRegistry()
	if err != nil {
		return nil, err
	}
	client := replyClient(reachable(cfg.MCPPrivateNetworks))
	return gateway.ReplyJob(gateway.NewSender(dbStore, sec, reg, client, logger)), nil
}

// replyClient follows no redirect: a 302 would turn the POST into a GET
// somewhere the URL check never saw. The redirect is the answer, and not a 2xx.
func replyClient(allow func(netip.Addr) bool) *http.Client {
	return &http.Client{
		Timeout:   replyTimeout,
		Transport: websearch.Transport(allow, "a reply"),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
