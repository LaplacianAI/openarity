package main

import "github.com/LaplacianAI/openarity/apps/cli/internal/config"

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

type settingView struct {
	Name   string `json:"name" yaml:"name"`
	Value  string `json:"value" yaml:"value"`
	Source string `json:"source" yaml:"source"`
}

func settingViews(settings config.Settings) []settingView {
	ordered := []config.Setting{
		settings.Context, settings.Server, settings.Theme,
		settings.Output, settings.Token,
	}

	views := make([]settingView, 0, len(ordered))
	for _, one := range ordered {
		views = append(views, settingView{
			Name:   one.Name,
			Value:  one.Value,
			Source: one.Source,
		})
	}
	return views
}
