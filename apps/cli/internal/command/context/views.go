package context

import "github.com/LaplacianAI/openarity/apps/cli/internal/config"

// The shape a context is printed as, which is not the shape it is stored as.
// HasToken rather than the token: this is a command someone runs while screen
// sharing.
type contextView struct {
	Name     string `json:"name" yaml:"name"`
	Server   string `json:"server" yaml:"server"`
	Active   bool   `json:"active" yaml:"active"`
	HasToken bool   `json:"has_token" yaml:"has_token"`
}

func contextViews(saved config.Config) []contextView {
	active := saved.ActiveName()
	names := saved.ContextNames()

	views := make([]contextView, 0, len(names))
	for _, name := range names {
		one := saved.Contexts[name]

		// Resolved here rather than emitted empty, so a consumer never has to
		// know the fallback rule to use the value.
		serverURL := one.Server
		if serverURL == "" {
			serverURL = config.DefaultServer
		}

		views = append(views, contextView{
			Name:     name,
			Server:   serverURL,
			Active:   name == active,
			HasToken: one.Token != "",
		})
	}
	return views
}
