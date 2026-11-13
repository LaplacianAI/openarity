package stream

import _ "embed"

// Schema is the catalog as JSON Schema: each kind's data object under $defs,
// by its name. A server that documents its stream in its own spec can check
// that spec against this, so the two cannot drift.
//
//go:embed schema.json
var Schema []byte
