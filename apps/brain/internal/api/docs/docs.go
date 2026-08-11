package docs

import (
	"log/slog"
	"net/http"

	apispec "github.com/LaplacianAI/openarity/apps/brain/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
)

// server-url is "/" so "try it" calls the origin the page was loaded from
// rather than the absolute address written in the spec. Those differ whenever
// the stack is reached by any other name — a LAN address, a port forward — and
// the request is then cross-origin, which the brain sends no CORS headers for.
// The browser reports that as a bare network failure with nothing useful in it.
const page = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Openarity brain API</title>
  <script
    type="module"
    src="https://unpkg.com/rapidoc@9.3.8/dist/rapidoc-min.js"
    integrity="sha384-szzoYhqSJqZH8X90KwbBjYL1CMLEa3U7xl/oAZg4ViKpYEY5GVhM+peBQB4u7ANM"
    crossorigin="anonymous"></script>
</head>
<body>
  <rapi-doc
    spec-url="/openapi.yaml"
    server-url="/"
    render-style="read"
    show-header="false"
    allow-server-selection="false"
    allow-authentication="true"
    persist-auth="false"
    theme="dark"></rapi-doc>
</body>
</html>
`

type handler struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *api.Router {
	h := &handler{logger: logger}

	r := api.NewPublicRouter("")
	r.Get("/openapi.yaml", h.spec)
	r.Get("/docs", h.ui)
	return r
}

func (h *handler) spec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	if _, err := w.Write(apispec.Spec); err != nil {
		h.logger.Error("failed to write the spec", "error", err)
	}
}

func (h *handler) ui(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write([]byte(page)); err != nil {
		h.logger.Error("failed to write the docs page", "error", err)
	}
}
