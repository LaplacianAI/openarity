package channels

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	pairByQR   = "qr"
	pairByCode = "code"

	// leaseStale is how long a holding role may go without renewing before
	// it is taken for gone: internal/whatsapp's lease.
	leaseStale = 60 * time.Second
)

// PairingStore is what linking a number reads and writes.
type PairingStore interface {
	RequestWhatsAppPairing(ctx context.Context, arg db.RequestWhatsAppPairingParams) error
	GetWhatsAppAccount(ctx context.Context, arg db.GetWhatsAppAccountParams) (db.GetWhatsAppAccountRow, error)
}

// pair asks for the channel's number to be linked, by QR or by a code for a
// number. The role holding the channel takes the request within 15 seconds
// and writes what to show; GET reads it. Pairing again replaces the number
// linked before.
func (h *handler) pair(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	row, ok := h.channelInTeam(w, r)
	if !ok {
		return
	}
	if !h.providers.HoldsConnection(row.Provider) {
		http.Error(w, "the "+row.Provider+" provider is not linked by pairing", http.StatusBadRequest)
		return
	}

	var req pairRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	var phone *string
	switch req.Method {
	case pairByQR:
		if req.Phone != nil {
			http.Error(w, "phone is only for method code", http.StatusBadRequest)
			return
		}
	case pairByCode:
		if req.Phone == nil {
			http.Error(w, "phone is required for method code: the number to link, with its country code", http.StatusBadRequest)
			return
		}
		digits, ok := phoneDigits(*req.Phone)
		if !ok {
			http.Error(w, "phone must be a number with its country code, 7 to 15 digits, such as +91 98765 43210", http.StatusBadRequest)
			return
		}
		phone = &digits
	default:
		http.Error(w, `method is "qr" or "code"`, http.StatusBadRequest)
		return
	}

	if err := h.pairing.RequestWhatsAppPairing(r.Context(), db.RequestWhatsAppPairingParams{
		ChannelID: row.ID, Method: &req.Method, Phone: phone,
	}); err != nil {
		api.Fail(w, h.logger, u, "failed to ask for a pairing", err)
		return
	}
	h.writePairing(w, r, row.ID, http.StatusAccepted)
}

// getPairing is what the channel's pairing shows now: poll it every few
// seconds while pairing, since a QR changes about every 20.
func (h *handler) getPairing(w http.ResponseWriter, r *http.Request) {
	row, ok := h.channelInTeam(w, r)
	if !ok {
		return
	}
	if !h.providers.HoldsConnection(row.Provider) {
		http.Error(w, "the "+row.Provider+" provider is not linked by pairing", http.StatusBadRequest)
		return
	}
	h.writePairing(w, r, row.ID, http.StatusOK)
}

func (h *handler) writePairing(w http.ResponseWriter, r *http.Request, channel uuid.UUID, status int) {
	acc, err := h.pairing.GetWhatsAppAccount(r.Context(), db.GetWhatsAppAccountParams{
		ChannelID: channel, StaleSeconds: leaseStale.Seconds(),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No role has looked at it, and nobody asked to pair it.
		api.WriteJSON(w, h.logger, status, pairing{State: "unpaired"})
		return
	case err != nil:
		api.Fail(w, h.logger, api.Caller(r), "failed to read the pairing", err)
		return
	}
	api.WriteJSON(w, h.logger, status, pairing{
		State: acc.State, Method: deref(acc.PairMethod), QR: deref(acc.PairQr), Code: deref(acc.PairCode),
		Phone: deref(acc.Phone), Error: deref(acc.PairError), Held: acc.Held, UpdatedAt: &acc.UpdatedAt,
	})
}

// phoneDigits is a number as WhatsApp takes it for a pairing code: digits
// alone, its country code first. Spaces, dashes, dots, brackets and a
// leading + are how people write one, and are dropped.
func phoneDigits(raw string) (string, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(raw), "+")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", false
		}
	}
	d := b.String()
	if len(d) < 7 || len(d) > 15 || d[0] == '0' {
		return "", false
	}
	return d, true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
