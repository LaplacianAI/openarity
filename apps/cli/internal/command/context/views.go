package context

import (
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
)

type contextView struct {
	Name     string `json:"name" yaml:"name"`
	Server   string `json:"server" yaml:"server"`
	Active   bool   `json:"active" yaml:"active"`
	HasToken bool   `json:"has_token" yaml:"has_token"`
}

func contextViews(saved config.Config, creds credential.Store) ([]contextView, error) {
	active := saved.ActiveName()
	names := saved.ContextNames()

	views := make([]contextView, 0, len(names))
	for _, name := range names {
		one := saved.Contexts[name]

		cred, err := creds.Get(name)
		if err != nil {
			return nil, err
		}

		serverURL := one.Server
		if serverURL == "" {
			serverURL = config.DefaultServer
		}

		views = append(views, contextView{
			Name:     name,
			Server:   serverURL,
			Active:   name == active,
			HasToken: !cred.IsZero(),
		})
	}
	return views, nil
}
