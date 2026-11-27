package schedules

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers"
)

// NewHooks is the webhook watchers' URL, on the webhook listener only: it
// carries no token, so the signature is all that admits a delivery (§7). It
// answers 202 once the delivery is kept, whatever became of it, so a caller
// learns nothing of the team's limits.
func NewHooks(logger *slog.Logger, service *triggers.Service) *api.Router {
	r := api.NewPublicRouter("/hooks/watchers")
	r.Post("/{hookID}", func(w http.ResponseWriter, r *http.Request) {
		received := time.Now()
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, triggers.MaxDelivery))
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		case err != nil:
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		err = service.Deliver(r.Context(), r.PathValue("hookID"), triggers.Delivery{
			Header: r.Header, Body: body, ReceivedAt: received,
		})
		var status int
		switch triggers.KindOf(err) {
		case 0:
			if err == nil {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			logger.ErrorContext(r.Context(), "A webhook delivery was not kept", "error", err)
			http.Error(w, "try again", http.StatusInternalServerError)
			return
		case triggers.NotFound:
			status = http.StatusNotFound
		case triggers.Conflict:
			status = http.StatusConflict
		case triggers.Unauthorized:
			status = http.StatusUnauthorized
		case triggers.Unavailable:
			status = http.StatusServiceUnavailable
		case triggers.Invalid, triggers.Limit, triggers.Forbidden:
			status = http.StatusBadRequest
		}
		http.Error(w, http.StatusText(status), status)
	})
	return r
}
