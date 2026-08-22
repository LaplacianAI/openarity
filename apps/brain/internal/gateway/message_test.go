package gateway

import "testing"

// Every field of an Inbound beyond the four an adapter must always fill is
// optional, so the zero value has to be safe to read rather than something a
// consumer guards against. These pin that, because the alternative is a nil
// check in every renderer and one of them eventually missing it.

func TestTheZeroEnrichmentIsSafeToRead(t *testing.T) {
	t.Parallel()

	var e Enrichment

	if len(e.Replace) != 0 {
		t.Errorf("Replace = %v, want empty", e.Replace)
	}
	// A nil map reads as the zero value; a nil pointer would panic here, and
	// that is the whole reason Replace is a map.
	if got := e.Replace["<@U01AA>"]; got != "" {
		t.Errorf("Replace[missing] = %q, want empty", got)
	}
	for range e.Replace {
		t.Error("ranged over a nil map")
	}
}

func TestAMessageWithNoOptionalFieldsIsWellFormed(t *testing.T) {
	t.Parallel()

	in := Inbound{
		ExternalID:   "D04ABC:1755412345.123456",
		Author:       Author{Ref: "U01AA"},
		Conversation: Conversation{Ref: "D04ABC", Kind: ConversationDirect},
		Text:         "what's our deploy status?",
	}

	if len(in.Mentions) != 0 {
		t.Errorf("Mentions = %v, want none", in.Mentions)
	}
	if len(in.Attachments) != 0 {
		t.Errorf("Attachments = %v, want none", in.Attachments)
	}
	if in.ReplyTo != "" {
		t.Errorf("ReplyTo = %q, want empty", in.ReplyTo)
	}
	if len(in.Enrichment.Replace) != 0 {
		t.Errorf("Enrichment.Replace = %v, want empty", in.Enrichment.Replace)
	}
	if !in.SentAt.IsZero() {
		t.Errorf("SentAt = %v, want the zero time", in.SentAt)
	}
}

func valid() Inbound {
	return Inbound{
		ExternalID:   "m-1",
		Author:       Author{Ref: "u-1"},
		Conversation: Conversation{Ref: "c-1", Kind: ConversationDirect},
	}
}

func TestAWellFormedMessageValidates(t *testing.T) {
	t.Parallel()

	if err := valid().Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// These are the four facts the handler goes on to use, so a message missing
// one fails later in a place that names neither the adapter nor the sender.
// The rules live here rather than in each adapter because they are the same
// rules whoever produced the message.
func TestValidateRefusesAMessageTheHandlerCannotUse(t *testing.T) {
	t.Parallel()

	for name, damage := range map[string]func(*Inbound){
		"no external id":      func(in *Inbound) { in.ExternalID = "" },
		"no author ref":       func(in *Inbound) { in.Author.Ref = "" },
		"no conversation ref": func(in *Inbound) { in.Conversation.Ref = "" },
		"no conversation kind": func(in *Inbound) {
			in.Conversation.Kind = ""
		},
		"an invented conversation kind": func(in *Inbound) {
			in.Conversation.Kind = "channel"
		},
		"a mention of nobody": func(in *Inbound) {
			in.Mentions = []Mention{{DisplayName: "Asha"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			in := valid()
			damage(&in)

			if err := in.Validate(); err == nil {
				t.Errorf("Validate accepted a message with %s", name)
			}
		})
	}
}

// Every optional field left empty is still a valid message — a location with
// no text, a message nobody was mentioned in.
func TestValidateDoesNotRequireTheOptionalFields(t *testing.T) {
	t.Parallel()

	in := valid()
	in.Text = ""
	in.ReplyTo = ""
	in.Mentions = nil
	in.Attachments = nil

	if err := in.Validate(); err != nil {
		t.Errorf("Validate refused a message with only the required fields: %v", err)
	}
}

// The kinds are compared, stored and rendered as these strings, so they are
// not free to change once a channel exists.
func TestTheConversationKindsAreStable(t *testing.T) {
	t.Parallel()

	for kind, want := range map[ConversationKind]string{
		ConversationDirect: "direct",
		ConversationGroup:  "group",
		ConversationThread: "thread",
	} {
		if string(kind) != want {
			t.Errorf("kind = %q, want %q", kind, want)
		}
	}
}
