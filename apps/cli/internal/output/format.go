package output

import "strings"

type Format string

const (
	Table Format = "table"
	JSON  Format = "json"
	YAML  Format = "yaml"
)

const Default = Table

func Parse(value string) (Format, bool) {
	switch Format(strings.ToLower(strings.TrimSpace(value))) {
	case Table:
		return Table, true
	case JSON:
		return JSON, true
	case YAML:
		return YAML, true
	default:
		return Default, false
	}
}

func All() []Format {
	return []Format{Table, JSON, YAML}
}

func Names() string {
	names := make([]string, 0, len(All()))
	for _, one := range All() {
		names = append(names, string(one))
	}
	return strings.Join(names, ", ")
}

func (f Format) IsMachine() bool {
	return f != Table
}
