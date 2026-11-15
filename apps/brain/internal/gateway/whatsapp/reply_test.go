package whatsapp

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

func replier(t *testing.T) gateway.Replier {
	t.Helper()
	r, ok := New().(gateway.Replier)
	if !ok {
		t.Fatal("whatsapp sends no replies")
	}
	return r
}

func notifier(t *testing.T) gateway.Notifier {
	t.Helper()
	n, ok := New().(gateway.Notifier)
	if !ok {
		t.Fatal("whatsapp sends no notices")
	}
	return n
}

func parts(t *testing.T, out gateway.Outbound) []Part {
	t.Helper()
	reqs, err := replier(t).Reply(gateway.Channel{}, out, nil, time.Time{})
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	ps := make([]Part, len(reqs))
	for i, r := range reqs {
		if ps[i], err = ReadPart(r); err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
	}
	return ps
}

func answer(text string) gateway.Outbound {
	return gateway.Outbound{
		ID: "run-1", Session: gateway.Session{Ref: "919876543210@s.whatsapp.net", Kind: gateway.SessionDirect},
		InReplyTo: "3EB0C767", Kind: "answer", Text: text,
	}
}

func TestAShortAnswerIsOnePartQuotingTheMessage(t *testing.T) {
	got := parts(t, answer("It waited on the **billing** lock."))
	want := []Part{{ID: "run-1:0", To: "919876543210@s.whatsapp.net", Text: "It waited on the *billing* lock.", Quote: "3EB0C767"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parts = %+v\nwant %+v", got, want)
	}
}

func TestALongAnswerIsSplitTheSameWayEveryTime(t *testing.T) {
	var b strings.Builder
	for i := range 300 {
		b.WriteString("line-")
		b.WriteString(strings.Repeat("x", i%40))
		b.WriteString("\n")
	}
	first, again := parts(t, answer(b.String())), parts(t, answer(b.String()))
	if len(first) < 2 || !reflect.DeepEqual(first, again) {
		t.Fatalf("split into %d parts, then %d; want several, the same each time", len(first), len(again))
	}
	var joined []string
	for i, p := range first {
		if n := utf8.RuneCountInString(p.Text); n > partRunes {
			t.Errorf("part %d is %d characters, over %d", i, n, partRunes)
		}
		if p.ID != "run-1:"+string(rune('0'+i)) && i < 10 {
			t.Errorf("part %d has id %q", i, p.ID)
		}
		if (i == 0) != (p.Quote != "") {
			t.Errorf("part %d quotes %q; only the first quotes", i, p.Quote)
		}
		joined = append(joined, p.Text)
	}
	if strings.Join(joined, "\n") != strings.TrimRight(b.String(), "\n") {
		t.Error("the parts, put back together, are not the answer")
	}
}

// A code block cut in two is closed in the first part and reopened in the
// next, so neither reads half as code.
func TestACodeBlockCutInTwoIsClosedAndReopened(t *testing.T) {
	code := "```\n" + strings.Repeat("print('hello world')\n", 400) + "```"
	ps := parts(t, answer("Here:\n"+code+"\nDone."))
	if len(ps) < 2 {
		t.Fatalf("one part; want the block split")
	}
	for i, p := range ps {
		if fences(p.Text)%2 != 0 {
			t.Errorf("part %d leaves a code block open:\n…%s", i, p.Text[max(0, len(p.Text)-40):])
		}
		if n := utf8.RuneCountInString(p.Text); n > partRunes {
			t.Errorf("part %d is %d characters", i, n)
		}
	}
	if !strings.HasPrefix(ps[1].Text, "```\n") {
		t.Errorf("part 2 starts %q; want the block reopened", ps[1].Text[:10])
	}
}

func TestAWordLongerThanAPartIsCutAnyway(t *testing.T) {
	ps := parts(t, answer(strings.Repeat("é", partRunes*2+5)))
	if len(ps) != 3 {
		t.Fatalf("%d parts; want 3", len(ps))
	}
	total := 0
	for _, p := range ps {
		if !utf8.ValidString(p.Text) {
			t.Fatal("a part was cut inside a character")
		}
		total += utf8.RuneCountInString(p.Text)
	}
	if total != partRunes*2+5 {
		t.Errorf("the parts hold %d characters; want every one of %d", total, partRunes*2+5)
	}
}

func TestAStepQuotesNothing(t *testing.T) {
	out := answer("Using `ledger`…")
	out.Kind, out.InReplyTo, out.ID = "event", "", "run-1:4"
	if got := parts(t, out); len(got) != 1 || got[0].Quote != "" || got[0].ID != "run-1:4:0" || got[0].Text != "Using `ledger`…" {
		t.Fatalf("parts = %+v; want one, quoting nothing, code kept", got)
	}
}

func TestAReplyThatCannotBeSentIsRefused(t *testing.T) {
	for name, out := range map[string]gateway.Outbound{
		"no conversation": {ID: "r", Text: "hi"},
		"not an address":  {ID: "r", Text: "hi", Session: gateway.Session{Ref: "telegram:123"}},
		"a status":        {ID: "r", Text: "hi", Session: gateway.Session{Ref: "status@broadcast"}},
		"a channel":       {ID: "r", Text: "hi", Session: gateway.Session{Ref: "1203630@newsletter"}},
		"nothing to say":  {ID: "r", Text: " \n", Session: gateway.Session{Ref: "919876543210@s.whatsapp.net"}},
	} {
		if reqs, err := replier(t).Reply(gateway.Channel{}, out, nil, time.Time{}); err == nil {
			t.Errorf("%s: built %d parts; want it refused", name, len(reqs))
		}
	}
}

func TestOnlyAPartIsReadBack(t *testing.T) {
	for name, req := range map[string]gateway.OutboundRequest{
		"an http request": {URL: "https://example.com", Body: []byte(`{"id":"a","to":"b","text":"c"}`)},
		"not json":        {URL: SendURL, Body: []byte(`{`)},
		"no text":         {URL: SendURL, Body: []byte(`{"id":"a","to":"b"}`)},
	} {
		if _, err := ReadPart(req); err == nil {
			t.Errorf("%s was read as a part", name)
		}
	}
}

func TestTheProviderHoldsAConnectionAndTakesNoWebhook(t *testing.T) {
	p := New()
	reg, err := gateway.NewRegistry(p)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if !reg.HoldsConnection(Name) || !reg.RepliesWithEvents(Name) {
		t.Error("whatsapp is not registered as a socket that sends steps")
	}
	if _, ok := reg.Notifier(Name); !ok {
		t.Error("whatsapp sends no notices")
	}
	if err := p.Verify(gateway.WebhookRequest{}, nil); err == nil {
		t.Error("a webhook verified")
	}
	if _, err := p.Parse(gateway.WebhookRequest{Body: []byte(`{}`)}, nil); err == nil {
		t.Error("a webhook parsed")
	}
}
