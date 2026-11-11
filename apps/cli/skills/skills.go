// Package skills is the agent skills oa ships: instructions in the Agent
// Skills format (a folder holding SKILL.md) that teach a coding agent, in any
// harness that reads the format, to work with Openarity through oa. They are
// built into the binary so the copy installed always matches the oa beside it.
package skills

import "embed"

// FS holds each skill as a folder at its root: build-a-team/SKILL.md.
//
//go:embed build-a-team
var FS embed.FS
