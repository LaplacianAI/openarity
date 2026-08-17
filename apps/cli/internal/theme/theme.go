package theme

import "strings"

type Theme string

const (
	Auto  Theme = "auto"
	Dark  Theme = "dark"
	Light Theme = "light"
)

func Parse(value string) (Theme, bool) {
	switch Theme(strings.ToLower(strings.TrimSpace(value))) {
	case Dark:
		return Dark, true
	case Light:
		return Light, true
	case Auto:
		return Auto, true
	default:
		return Auto, false
	}
}

func All() []Theme {
	return []Theme{Auto, Dark, Light}
}

func Names() string {
	names := make([]string, 0, len(All()))
	for _, one := range All() {
		names = append(names, string(one))
	}
	return strings.Join(names, ", ")
}
