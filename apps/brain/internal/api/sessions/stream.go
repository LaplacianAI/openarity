package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/stream"
)

// events follows a session: its messages and the events of every run in it,
// after Last-Event-ID or ?after=, then as they happen. Visible to whoever may
// read the session.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	session, ok := h.visible(w, r, teamID)
	if !ok {
		return
	}
	after, err := stream.Resume(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log := sessionLog{store: h.store, session: session.ID}
	if err := (stream.Follower{Log: log}).Follow(w, r, after); err != nil {
		h.logger.ErrorContext(r.Context(), "Following a session", "session_id", session.ID, "error", err)
	}
}

type sessionLog struct {
	store   Store
	session uuid.UUID
}

func (l sessionLog) Since(ctx context.Context, after int64, limit int) ([]stream.Envelope, error) {
	rows, err := l.store.ListSessionEvents(ctx, db.ListSessionEventsParams{
		SessionID: l.session, After: after, PageSize: int32(limit), //nolint:gosec // the follower's page, 100
	})
	if err != nil {
		return nil, err
	}
	out := make([]stream.Envelope, 0, len(rows))
	for _, row := range rows {
		env, err := envelopeOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}

// errNoSeq is a row the query returned unnumbered, which its filter excludes.
var errNoSeq = errors.New("a session event with no sequence number")

// envelopeOf is one stored row as the stream's event. A run event of a kind
// the catalog has no event for is a custom one, named by its kind, carrying
// what was recorded.
func envelopeOf(row db.ListSessionEventsRow) (stream.Envelope, error) {
	if row.Seq == nil {
		return stream.Envelope{}, errNoSeq
	}
	env := stream.Envelope{Seq: *row.Seq, At: row.At}
	if row.RunID != nil {
		env.RunID = row.RunID.String()
	}
	e, err := eventOf(row)
	if err != nil {
		return stream.Envelope{}, fmt.Errorf("session event %d, a %s: %w", *row.Seq, row.Kind, err)
	}
	env.Event = e
	return env, nil
}

func eventOf(row db.ListSessionEventsRow) (stream.Event, error) {
	switch row.Kind {
	case "message":
		var p struct {
			ID     string `json:"id"`
			UserID string `json:"user_id"`
			Text   string `json:"text"`
			Asks   bool   `json:"asks"`
		}
		if err := json.Unmarshal(row.Payload, &p); err != nil {
			return nil, err
		}
		m := stream.Message{ID: p.ID, UserID: p.UserID, Parts: []stream.Part{{Type: stream.PartText, Text: p.Text}}}
		if p.Asks {
			m.State = stream.StatePending
		}
		return m, nil
	case "started":
		e := stream.RunStarted{}
		if row.Agent != nil {
			e.Agent = *row.Agent
		}
		if row.ParentRunID != nil {
			e.ParentRunID = row.ParentRunID.String()
		}
		return e, nil
	case "step":
		return decode[stream.StepStarted](row.Payload)
	case "text", "reasoning":
		var p struct {
			Text      string `json:"text"`
			Truncated bool   `json:"truncated"`
			Step      int    `json:"step"`
		}
		if err := json.Unmarshal(row.Payload, &p); err != nil {
			return nil, err
		}
		if row.Kind == "reasoning" {
			return stream.ReasoningChunk{MessageID: messageID(row, p.Step, stream.PartOfReasoning), Delta: p.Text, Truncated: p.Truncated}, nil
		}
		return stream.TextMessageChunk{MessageID: messageID(row, p.Step, stream.PartOfText), Delta: p.Text, Truncated: p.Truncated}, nil
	case "tool_call":
		var p struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Truncated bool   `json:"truncated"`
		}
		if err := json.Unmarshal(row.Payload, &p); err != nil {
			return nil, err
		}
		return stream.ToolCallChunk{ToolCallID: p.ID, Name: p.Name, Arguments: p.Arguments, Truncated: p.Truncated}, nil
	case "tool_result":
		var p struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Output     string `json:"output"`
			Error      string `json:"error"`
			Truncated  bool   `json:"truncated"`
			DurationMS int64  `json:"duration_ms"`
		}
		if err := json.Unmarshal(row.Payload, &p); err != nil {
			return nil, err
		}
		return stream.ToolCallResult{
			ToolCallID: p.ID, Name: p.Name, Content: p.Output, Error: p.Error,
			Truncated: p.Truncated, DurationMS: p.DurationMS,
		}, nil
	case "usage":
		return decode[stream.Usage](row.Payload)
	case "compacted":
		return decode[stream.Compacted](row.Payload)
	case "finished":
		e, err := decode[stream.RunFinished](row.Payload)
		if err != nil {
			return nil, err
		}
		if row.Output != nil {
			e.Output = *row.Output
		}
		return e, nil
	case "consumed":
		return decode[stream.MessageConsumed](row.Payload)
	}
	return stream.Custom{Name: row.Kind, Value: row.Payload}, nil
}

// messageID names the model call a piece of writing came from. A row recorded
// before writing kept its step has none, and a reader groups it by the
// step_started before it instead.
func messageID(row db.ListSessionEventsRow, step int, part string) string {
	if step == 0 || row.RunID == nil {
		return ""
	}
	return stream.MessageID(row.RunID.String(), step, part)
}

// decode reads a payload the runtime wrote with the same field names as the
// event it becomes.
func decode[T stream.Event](payload []byte) (T, error) {
	var e T
	err := json.Unmarshal(payload, &e)
	return e, err
}
