package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
)

// hook posts one message to a custom channel's webhook, signed as the provider
// would sign it, and returns the status the brain answered.
func hook(t *testing.T, webhook string, channel uuid.UUID, secret string, msg map[string]any) int {
	t.Helper()

	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://"+webhook+"/hooks/custom/"+channel.String(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(custom.HeaderTimestamp, ts)
	req.Header.Set(custom.HeaderSignature, custom.Sign(secret, ts, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST hook: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func asks(id, text string) map[string]any {
	return map[string]any{
		"id":      id,
		"text":    text,
		"author":  map[string]any{"ref": "U-asha", "display_name": "Asha"},
		"session": map[string]any{"ref": "D-asha", "kind": "direct"},
	}
}

// waitForReplies polls the outbox until it holds n replies, or fails.
func (b *brain) waitForReplies(t *testing.T, n int, within time.Duration) {
	t.Helper()

	deadline := time.Now().Add(within)
	for b.count(t, `SELECT count(*) FROM replies`) < n {
		if time.Now().After(deadline) {
			t.Fatalf("the outbox holds %d replies after %s, want %d", b.count(t, `SELECT count(*) FROM replies`), within, n)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The whole of it, through the binary as it ships: a signed message on a
// channel starts the team's hub, the worker runs it against the model, and the
// answer is in the outbox for the conversation. The same message delivered
// again starts nothing; the next message is answered knowing what was said.
func TestAChannelMessageIsAnsweredInItsConversation(t *testing.T) {
	b := newBrain(t, newScriptedLLM(t))
	webhook := os.Getenv("OPENARITY_WEBHOOK_BIND")

	var dev uuid.UUID
	if err := b.raw.QueryRow(t.Context(),
		`SELECT id FROM users WHERE issuer = 'dev' AND subject = 'dev'`).Scan(&dev); err != nil {
		t.Fatalf("the development user: %v", err)
	}
	if _, err := b.raw.Exec(t.Context(),
		`UPDATE team_members SET role = 'admin' WHERE team_id = $1 AND user_id = $2`, b.team, dev); err != nil {
		t.Fatalf("making the development user an admin: %v", err)
	}
	if _, err := b.raw.Exec(t.Context(), `
		INSERT INTO team_settings (team_id, model_name, model_max_tokens) VALUES ($1, 'test/model', 1024)`,
		b.team); err != nil {
		t.Fatalf("giving the hub a model: %v", err)
	}

	status, raw := b.call(t, http.MethodPost, "/teams/"+b.team.String()+"/channels",
		map[string]string{"provider": "custom", "name": "support"})
	if status != http.StatusCreated {
		t.Fatalf("POST channels: %d %s", status, raw)
	}
	var ch struct {
		ID            uuid.UUID `json:"id"`
		SigningSecret string    `json:"signing_secret"`
	}
	if err := json.Unmarshal(raw, &ch); err != nil || ch.SigningSecret == "" {
		t.Fatalf("decode the channel: %v (%s)", err, raw)
	}
	if _, err := b.raw.Exec(t.Context(),
		`INSERT INTO channel_senders (channel_id, sender_ref, user_id) VALUES ($1, 'U-asha', $2)`, ch.ID, dev); err != nil {
		t.Fatalf("approving the sender: %v", err)
	}

	stopWorker := startWorker(t)
	defer stopWorker()

	if got := hook(t, webhook, ch.ID, ch.SigningSecret, asks("m-1", "Why did the export fail?")); got != http.StatusOK {
		t.Fatalf("the first message was answered %d, want 200", got)
	}
	b.waitForReplies(t, 1, 90*time.Second)

	var kind, text string
	if err := b.raw.QueryRow(t.Context(), `SELECT kind, text FROM replies`).Scan(&kind, &text); err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	if kind != "answer" || text != finalAnswer {
		t.Errorf("reply = %s %q, want the model's answer %q", kind, text, finalAnswer)
	}

	// The provider did not see the 200, and sends it again.
	if got := hook(t, webhook, ch.ID, ch.SigningSecret, asks("m-1", "Why did the export fail?")); got != http.StatusOK {
		t.Fatalf("the redelivery was answered %d, want 200", got)
	}
	if n := b.count(t, `SELECT count(*) FROM runs WHERE reply_to_channel`); n != 1 {
		t.Errorf("%d channel runs after a redelivery, want 1", n)
	}

	if got := hook(t, webhook, ch.ID, ch.SigningSecret, asks("m-2", "Was it the nightly one?")); got != http.StatusOK {
		t.Fatalf("the second message was answered %d, want 200", got)
	}
	b.waitForReplies(t, 2, 90*time.Second)
	if !b.llm.saw("The conversation so far") || !b.llm.saw("Why did the export fail?") {
		t.Error("the second run was not shown the conversation before it")
	}
	if n := b.count(t, `SELECT count(*) FROM runs WHERE reply_to_channel AND status = 'succeeded'`); n != 2 {
		t.Errorf("%d channel runs succeeded, want 2", n)
	}
}
