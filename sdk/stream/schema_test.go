package stream

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type schemaDef struct {
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
}

func defs(t *testing.T) map[string]schemaDef {
	t.Helper()
	var s struct {
		Defs map[string]schemaDef `json:"$defs"`
	}
	if err := json.Unmarshal(Schema, &s); err != nil {
		t.Fatal(err)
	}
	return s.Defs
}

// The schema describes exactly the kinds there are: one added to the Go
// types and not to the schema, or the other way round, fails here.
func TestTheSchemaHasEveryKindAndNoOther(t *testing.T) {
	var got []string
	for name := range defs(t) {
		if name != "envelope" && name != "part" {
			got = append(got, name)
		}
	}
	var want []string
	for _, k := range Kinds() {
		want = append(want, string(k))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the schema has %v, the catalog %v", got, want)
	}
}

// Each kind's properties are its Go fields' JSON names and the envelope's;
// its required ones are those Go always writes.
func TestTheSchemaMatchesEachEvent(t *testing.T) {
	all := defs(t)
	for _, e := range samples() {
		def := all[string(e.Kind())]
		checkFields(t, string(e.Kind()), def, reflect.TypeOf(e), "at", "run_id")
	}
	checkFields(t, "part", all["part"], reflect.TypeFor[Part]())
}

func checkFields(t *testing.T, name string, def schemaDef, typ reflect.Type, extra ...string) {
	t.Helper()
	var props, required []string
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		field, opts, _ := strings.Cut(tag, ",")
		props = append(props, field)
		if !strings.Contains(opts, "omitempty") {
			required = append(required, field)
		}
	}
	props = append(props, extra...)
	var got []string
	for p := range def.Properties {
		got = append(got, p)
	}
	slices.Sort(got)
	slices.Sort(props)
	if !slices.Equal(got, props) {
		t.Errorf("%s: the schema has %v, Go has %v", name, got, props)
	}
	gotReq := slices.Clone(def.Required)
	slices.Sort(gotReq)
	slices.Sort(required)
	if !slices.Equal(gotReq, required) {
		t.Errorf("%s: the schema requires %v, Go always writes %v", name, gotReq, required)
	}
}
