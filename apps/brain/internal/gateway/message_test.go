package gateway

import (
	"strings"
	"testing"
)

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
		ExternalID: "D04ABC:1755412345.123456",
		Author:     Author{Ref: "U01AA"},
		Session:    Session{Ref: "D04ABC", Kind: SessionDirect},
		Text:       "what's our deploy status?",
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
		ExternalID: "m-1",
		Author:     Author{Ref: "u-1"},
		Session:    Session{Ref: "c-1", Kind: SessionDirect},
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
		"no external id": func(in *Inbound) { in.ExternalID = "" },
		"no author ref":  func(in *Inbound) { in.Author.Ref = "" },
		"no session ref": func(in *Inbound) { in.Session.Ref = "" },
		"no session kind": func(in *Inbound) {
			in.Session.Kind = ""
		},
		"an invented session kind": func(in *Inbound) {
			in.Session.Kind = "channel"
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
func TestTheSessionKindsAreStable(t *testing.T) {
	t.Parallel()

	for kind, want := range map[SessionKind]string{
		SessionDirect: "direct",
		SessionGroup:  "group",
		SessionThread: "thread",
	} {
		if string(kind) != want {
			t.Errorf("kind = %q, want %q", kind, want)
		}
	}
}

// A ref is stored exactly as sent, because it is an identity — clipping two
// distinct refs to the same value would merge two people. So an absurd one is
// refused rather than truncated. Without this a 1 MiB body is a 1 MiB ref,
// fifty per channel, written by anyone who knows the URL.
func TestARefLongerThanTheColumnAllowsIsRefused(t *testing.T) {
	t.Parallel()

	in := valid()
	in.Author.Ref = strings.Repeat("u", SenderRefMax+1)

	if err := in.Validate(); err == nil {
		t.Error("a ref longer than the column allows was accepted")
	}
}

func TestARefAtTheLimitIsAccepted(t *testing.T) {
	t.Parallel()

	in := valid()
	in.Author.Ref = strings.Repeat("u", SenderRefMax)

	if err := in.Validate(); err != nil {
		t.Errorf("a ref at the limit was refused: %v", err)
	}
}

// Mentions carry refs too, and they are resolved against the same table.
func TestAMentionRefIsBoundedAsWell(t *testing.T) {
	t.Parallel()

	in := valid()
	in.Mentions = []Mention{{SenderRef: strings.Repeat("u", SenderRefMax+1)}}

	if err := in.Validate(); err == nil {
		t.Error("a mention with an unbounded ref was accepted")
	}
}

// The limit counts characters, not bytes: the column is char_length, so a
// ref of multi-byte characters that passes here must also fit there.
func TestTheRefLimitCountsCharactersNotBytes(t *testing.T) {
	t.Parallel()

	in := valid()
	in.Author.Ref = strings.Repeat("é", SenderRefMax)

	if err := in.Validate(); err != nil {
		t.Errorf("a ref of %d characters was refused: %v", SenderRefMax, err)
	}
}
