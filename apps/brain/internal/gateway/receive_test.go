package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func noFiles(context.Context, []Attachment) ([]Stored, []string) { return nil, nil }

// The channel's scope goes with what is received: a stranger outside it is
// not queued, and the inbox decides answering by the same scope.
func TestAReceivedMessageIsTakenByItsChannelsScope(t *testing.T) {
	t.Parallel()

	reg, err := NewRegistry(stubProvider{name: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	s, sink := newFakeSenders(), &fakeSink{}
	r := NewReceiver(slog.New(slog.NewTextHandler(io.Discard, nil)), s, reg, sink, nil)
	p, _ := reg.Get("chat")
	ch := db.Channel{ID: uuid.New(), TeamID: uuid.New(), AnswersIn: string(AnswersInGroups)}

	if err := r.Receive(t.Context(), p, ch, []Inbound{message("u-new", "Asha")}, noFiles); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(s.recorded) != 0 {
		t.Error("a stranger in a direct chat was queued by a channel that answers in groups")
	}

	s.link(ch.ID, "u-1", uuid.New())
	if err := r.Receive(t.Context(), p, ch, []Inbound{message("u-1", "Asha")}, noFiles); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if sink.channel.AnswersIn != AnswersInGroups || sink.channel.ID != ch.ID {
		t.Errorf("the inbox was handed %+v; want the channel with its scope", sink.channel)
	}
}
