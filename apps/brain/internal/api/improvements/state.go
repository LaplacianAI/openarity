package improvements

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Snapshot is a proposal's target, whole, on one side of the change: an
// agent, or a skill. skill_new carries both after it: the agent, and the
// skill it creates and grants.
type Snapshot struct {
	Agent *AgentState `json:"agent,omitempty"`
	Skill *SkillState `json:"skill,omitempty"`
	// Set on a skill_new once it is applied: the skill it created, which
	// its revert deletes.
	CreatedSkill *uuid.UUID `json:"created_skill,omitempty"`
}

// AgentState is what a proposal may read and change of an agent: the
// request a person's PUT would send, grants included, and the tool notes.
type AgentState struct {
	Agent agents.Request `json:"agent"`
	Notes []ToolNote     `json:"notes"`
}

// ToolNote is the agents API's: the same note, said the same way.
type ToolNote = agents.ToolNote

// SkillState is a skill's manifest, body and files. A file is named by its
// hash; Text holds the content of a text file a proposal adds or changes,
// since a replace leaves the old objects to the reaper and a revert must
// still have them.
type SkillState struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       *string           `json:"license,omitempty"`
	Compatibility *string           `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  *string           `json:"allowed_tools,omitempty"`
	Body          string            `json:"body"`
	Source        string            `json:"source"`
	Files         []FileState       `json:"files"`
}

type FileState struct {
	Path      string  `json:"path"`
	MediaType string  `json:"media_type"`
	SHA256    string  `json:"sha256"`
	Text      *string `json:"text,omitempty"`
}

// cloned is a copy whose slices are its own, so putting it in order leaves
// the caller's untouched. A nil allow-list stays nil: it means every tool.
func (s AgentState) cloned() AgentState {
	s.Agent.MCPServers = slices.Clone(s.Agent.MCPServers)
	for i := range s.Agent.MCPServers {
		s.Agent.MCPServers[i].Allow = slices.Clone(s.Agent.MCPServers[i].Allow)
	}
	s.Agent.Skills = slices.Clone(s.Agent.Skills)
	s.Agent.Delegates = slices.Clone(s.Agent.Delegates)
	s.Notes = slices.Clone(s.Notes)
	return s
}

func (s AgentState) sortedNotes() []ToolNote {
	s.canonical()
	return s.Notes
}

// canonical puts a state in one order, so equal states compare equal. It
// sorts in place: callers holding someone else's state clone it first.
func (s *AgentState) canonical() {
	slices.SortFunc(s.Agent.MCPServers, func(a, b agents.ServerGrant) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	for i := range s.Agent.MCPServers {
		if a := s.Agent.MCPServers[i].Allow; a != nil {
			slices.Sort(a)
		}
	}
	slices.SortFunc(s.Agent.Skills, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	slices.SortFunc(s.Agent.Delegates, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	slices.SortFunc(s.Notes, func(a, b ToolNote) int {
		if c := bytes.Compare(a.Server[:], b.Server[:]); c != 0 {
			return c
		}
		return strings.Compare(a.Tool, b.Tool)
	})
	if s.Agent.MCPServers == nil {
		s.Agent.MCPServers = []agents.ServerGrant{}
	}
	if s.Agent.Skills == nil {
		s.Agent.Skills = []uuid.UUID{}
	}
	if s.Agent.Delegates == nil {
		s.Agent.Delegates = []uuid.UUID{}
	}
	if s.Notes == nil {
		s.Notes = []ToolNote{}
	}
}

func (s *SkillState) canonical() {
	slices.SortFunc(s.Files, func(a, b FileState) int { return strings.Compare(a.Path, b.Path) })
	if s.Files == nil {
		s.Files = []FileState{}
	}
	if len(s.Metadata) == 0 {
		s.Metadata = nil
	}
}

// sameAgent is whether two agent states are the same agent setup.
func sameAgent(a, b AgentState) bool {
	a, b = a.cloned(), b.cloned()
	a.canonical()
	b.canonical()
	return jsonEqual(a, b)
}

// sameSkill compares skills by content: a file is its path and its hash.
// The text is the proposal's copy, and the media type is sniffed from the
// bytes when they are stored, so neither says anything the hash does not.
func sameSkill(a, b SkillState) bool {
	a.Files, b.Files = slices.Clone(a.Files), slices.Clone(b.Files)
	a.canonical()
	b.canonical()
	return jsonEqual(withoutText(a), withoutText(b))
}

func withoutText(s SkillState) SkillState {
	files := make([]FileState, len(s.Files))
	for i, f := range s.Files {
		f.Text, f.MediaType = nil, ""
		files[i] = f
	}
	s.Files = files
	return s
}

func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// agentState reads an agent's state, with the queries of a transaction that
// has locked it or of a plain read.
func agentState(ctx context.Context, q *db.Queries, row db.Agent) (AgentState, error) {
	req, err := agents.RequestOf(row)
	if err != nil {
		return AgentState{}, err
	}

	servers, err := q.ListAgentMCPServers(ctx, row.ID)
	if err != nil {
		return AgentState{}, err
	}
	for _, s := range servers {
		req.MCPServers = append(req.MCPServers, agents.ServerGrant{ID: s.McpServerID, Allow: s.Allow})
	}
	if req.Skills, err = q.ListAgentSkills(ctx, row.ID); err != nil {
		return AgentState{}, err
	}
	if req.Delegates, err = q.ListAgentDelegates(ctx, row.ID); err != nil {
		return AgentState{}, err
	}

	notes, err := q.ListAgentToolNotes(ctx, row.ID)
	if err != nil {
		return AgentState{}, err
	}
	st := AgentState{Agent: req}
	for _, n := range notes {
		st.Notes = append(st.Notes, ToolNote{Server: n.McpServerID, Tool: n.Tool, Note: n.Note})
	}
	st.canonical()
	return st, nil
}

// writeAgent writes an agent state exactly as the agents API's replace does,
// and then its notes. The caller has locked the row.
func writeAgent(ctx context.Context, q *db.Queries, row db.Agent, st AgentState, loops []string) error {
	if err := agents.Validate(st.Agent, loops); err != nil {
		return ruleError{err.Error()}
	}
	params, err := agents.Columns(st.Agent)
	if err != nil {
		return err
	}
	if row, err = q.UpdateAgent(ctx, agents.UpdateParams(row.ID, params)); err != nil {
		return err
	}
	if err := agents.Regrant(ctx, q, row, st.Agent); err != nil {
		return err
	}

	if err := q.ClearAgentToolNotes(ctx, row.ID); err != nil {
		return err
	}
	for _, n := range st.Notes {
		if err := q.AddAgentToolNote(ctx, db.AddAgentToolNoteParams{
			AgentID: row.ID, TeamID: row.TeamID, McpServerID: n.Server, Tool: n.Tool, Note: n.Note,
		}); err != nil {
			return err
		}
	}
	return nil
}

func skillState(ctx context.Context, q *db.Queries, row db.Skill) (SkillState, error) {
	var meta map[string]string
	if len(row.Metadata) > 0 {
		if err := json.Unmarshal(row.Metadata, &meta); err != nil {
			return SkillState{}, fmt.Errorf("reading the skill's metadata: %w", err)
		}
	}
	files, err := q.ListSkillFiles(ctx, row.ID)
	if err != nil {
		return SkillState{}, err
	}

	st := SkillState{
		Name: row.Name, Description: row.Description, License: row.License,
		Compatibility: row.Compatibility, Metadata: meta, AllowedTools: row.AllowedTools,
		Body: row.Body, Source: row.Source,
	}
	for _, f := range files {
		st.Files = append(st.Files, FileState{Path: f.Path, MediaType: f.MediaType, SHA256: hex.EncodeToString(f.Sha256)})
	}
	st.canonical()
	return st, nil
}

// Getter reads a stored object's bytes.
type Getter interface {
	Get(ctx context.Context, teamID uuid.UUID, key string) ([]byte, error)
}

// directory builds the skill a state describes. A file the state carries the
// text of is that text; any other is read from the skill's stored files by
// its hash, and a file no longer stored is a target that changed.
func directory(ctx context.Context, o Getter, teamID uuid.UUID, st SkillState, stored []db.SkillFile) (skill.Directory, error) {
	// An upload's metadata is always a map, never null, and the table holds
	// it to an object.
	meta := st.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	m := skill.Manifest{
		Name: st.Name, Description: st.Description, License: st.License, Compatibility: st.Compatibility,
		Metadata: meta, AllowedTools: st.AllowedTools, Body: st.Body,
	}
	if err := m.Validate(); err != nil {
		return skill.Directory{}, ruleError{err.Error()}
	}

	dir := skill.Directory{Manifest: m}
	for _, f := range st.Files {
		var data []byte
		if f.Text != nil {
			data = []byte(*f.Text)
		} else {
			i := slices.IndexFunc(stored, func(s db.SkillFile) bool {
				return s.Path == f.Path && hex.EncodeToString(s.Sha256) == f.SHA256
			})
			if i < 0 {
				return skill.Directory{}, errChanged
			}
			b, err := o.Get(ctx, teamID, stored[i].ObjectKey)
			if err != nil {
				return skill.Directory{}, fmt.Errorf("reading %s: %w", f.Path, err)
			}
			data = b
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return skill.Directory{}, errChanged
		}
		mediaType := f.MediaType
		if mediaType == "" {
			mediaType = objects.Sniff(data)
		}
		dir.Files = append(dir.Files, skill.File{Path: f.Path, Data: data, SHA256: sum, MediaType: mediaType})
	}
	return dir, nil
}
