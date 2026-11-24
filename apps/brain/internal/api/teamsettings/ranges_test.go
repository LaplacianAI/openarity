package teamsettings

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// The replay numbers' ranges the API checks are those its description
// gives, in every schema that names them; the migration's checks hold the
// same (store's replays migration test).
func TestTheReplayRangesAreTheSpecs(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Minimum *int64 `yaml:"minimum"`
					Maximum *int64 `yaml:"maximum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for _, r := range replayRanges(settingsRequest{}) {
		seen := 0
		for name, schema := range spec.Components.Schemas {
			p, ok := schema.Properties[r.name]
			if !ok {
				continue
			}
			seen++
			if p.Minimum == nil || p.Maximum == nil || *p.Minimum != int64(r.min) || *p.Maximum != int64(r.max) {
				t.Errorf("%s.%s is %v to %v in the spec, %d to %d here", name, r.name, p.Minimum, p.Maximum, r.min, r.max)
			}
		}
		if seen == 0 {
			t.Errorf("the spec names no %s", r.name)
		}
	}
}
