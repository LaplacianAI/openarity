package agent

import (
	"encoding/json"
	"testing"
)

// A transcript is written to somebody's database the moment a Store exists, so
// the JSON shape stops being an implementation detail and becomes a format
// that has to survive a field being added. Asserting the exact bytes is what
// makes a rename a test failure rather than a migration nobody wrote.
func TestAStoredMessageHasAStableShape(t *testing.T) {
	raw, err := json.Marshal(Message{
		Role:    RoleAssistant,
		Content: []Content{{Type: ContentText, Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	const want = `{"role":"assistant","content":[{"type":"text","text":"hello"}]}`
	if string(raw) != want {
		t.Errorf("a stored message is\n  %s\nwant\n  %s", raw, want)
	}
}

// Every row carries the fields a message did not use, unless they are omitted.
// On a transcript of a few hundred messages that is the difference between a
// row and three of them.
func TestAMessageDoesNotStoreWhatItDoesNotHave(t *testing.T) {
	raw, err := json.Marshal(Message{Role: RoleUser})
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	const want = `{"role":"user"}`
	if string(raw) != want {
		t.Errorf("an empty message stored as %s, want %s", raw, want)
	}
}

// The outer omitempty is not enough: a message that has content still renders
// every field of it. A file or an image carries no text, and "text":"" on
// every one of them is bytes nobody reads.
func TestAContentBlockDoesNotStoreWhatItDoesNotHave(t *testing.T) {
	raw, err := json.Marshal(Content{
		Type: ContentFile,
		Blob: &Blob{MediaType: "application/pdf"},
	})
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	const want = `{"type":"file","blob":{"media_type":"application/pdf"}}`
	if string(raw) != want {
		t.Errorf("a file block stored as\n  %s\nwant\n  %s", raw, want)
	}
}

func TestAMessageSurvivesTheRoundTrip(t *testing.T) {
	original := Message{
		Role:       RoleTool,
		ToolCallID: "call_1",
		Content:    []Content{{Type: ContentText, Text: "done", Cacheable: true}},
		ToolCalls:  []ToolCall{{ID: "call_1", Name: "grep", Arguments: json.RawMessage(`{"q":"x"}`)}},
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var back Message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}

	if back.Role != RoleTool || back.ToolCallID != "call_1" || back.Text() != "done" {
		t.Errorf("round trip lost something: %+v", back)
	}
	if !back.Content[0].Cacheable {
		t.Error("Cacheable did not survive; a resumed run would stop marking its prefix")
	}
	if len(back.ToolCalls) != 1 || back.ToolCalls[0].Name != "grep" {
		t.Fatalf("the tool call did not survive: %+v", back.ToolCalls)
	}
	if string(back.ToolCalls[0].Arguments) != `{"q":"x"}` {
		t.Errorf("the arguments did not survive: %s", back.ToolCalls[0].Arguments)
	}
}

// A blob is the one field that is not text. Base64 round-tripping works until
// somebody stores a PDF, which is the first time anybody finds out.
func TestABlobSurvivesTheRoundTrip(t *testing.T) {
	original := Message{
		Role: RoleUser,
		Content: []Content{{
			Type: ContentFile,
			Blob: &Blob{
				MediaType: "application/pdf",
				Name:      "lease.pdf",
				Data:      []byte{0x25, 0x50, 0x44, 0x46},
			},
		}},
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var back Message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}

	blob := back.Content[0].Blob
	if blob == nil {
		t.Fatal("the blob is gone")
	}
	if blob.MediaType != "application/pdf" || blob.Name != "lease.pdf" {
		t.Errorf("blob metadata did not survive: %+v", blob)
	}
	if string(blob.Data) != "%PDF" {
		t.Errorf("blob bytes did not survive: %v", blob.Data)
	}
}
