package config

import "github.com/LaplacianAI/openarity/apps/cli/internal/config"

type settingView struct {
	Name   string `json:"name" yaml:"name"`
	Value  string `json:"value" yaml:"value"`
	Source string `json:"source" yaml:"source"`
}

// Source is what makes this worth printing: with four places to set one value,
// "I set it and it did not take" is unanswerable without saying which won.
// The token arrives already reported as "set (N characters)" — config.Resolve
// never puts the value in a Setting, so there is nothing to redact here.
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
