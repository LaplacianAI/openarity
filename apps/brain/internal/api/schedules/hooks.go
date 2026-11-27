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
// carries no token, so the signature is all that admits a delivery (§7). A
// signed delivery to an active watcher is answered 202 once it is kept,
// whether it started a run, was held or met the team's limits, so a caller
// learns nothing of them.
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
		if err == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		status, ok := statusOf(triggers.KindOf(err))
		if !ok {
			logger.ErrorContext(r.Context(), "A webhook delivery was not kept", "error", err)
			status = http.StatusInternalServerError
		}
		refused(w, status)
		http.Error(w, http.StatusText(status), status)
	})
	return r
}
