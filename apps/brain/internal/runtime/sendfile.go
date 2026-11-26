package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/stream"
)

const (
	// SendFileToolName hands the person a file of the conversation.
	SendFileToolName = "send_file"

	// filesPerRun is how many files one run hands its person: past it a run
	// is sending a folder, not answering.
	filesPerRun = 10
	// maxCaption is Telegram's caption, the shortest a platform takes.
	maxCaption = 1024
)

// FileSendStore is what handing a person a file takes: the file, looked up
// within the conversation, and the reply that posts it to a channel.
type FileSendStore interface {
	GetAttachmentInSession(ctx context.Context, arg db.GetAttachmentInSessionParams) (db.Attachment, error)
	InsertFileReply(ctx context.Context, arg db.InsertFileReplyParams) (int64, error)
	FileReplyExists(ctx context.Context, arg db.FileReplyExistsParams) (bool, error)
	CountRunFilesSent(ctx context.Context, runID uuid.UUID) (int64, error)
	GetSessionFileReach(ctx context.Context, sessionID uuid.UUID) (db.GetSessionFileReachRow, error)
}

// FileSends is where a run's files can go. Store and SendsFiles are
// required: SendsFiles says whether a channel provider posts a file, and the
// gateway's registry answers it. Logger hears of a file sent whose event was
// not recorded; with none, nothing is offered.
type FileSends struct {
	Store FileSendStore
	// MaxFile is the most a provider's channel takes, in bytes: 0 for one
	// that sends no files.
	MaxFile func(provider string) int64
	Logger  *slog.Logger
}

// sendFile is the tool. toChannel is a run answering a channel, whose file
// is also a reply; a conversation read on its stream has the event alone.
// note writes the run event and kick has a channel's reply sent now. With no
// store, as in a replay, the tool is a stand-in that is never made.
type sendFile struct {
	store     FileSendStore
	logger    *slog.Logger
	run       db.Run
	toChannel bool
	provider  string // the channel's, when toChannel
	maxFile   int64  // the most the channel takes, when toChannel
	note      func(ctx context.Context, kind string, payload any) error
	kick      func(callID string)
	// sent counts this attempt's tries at a file that was found, on top of
	// the files sent before a resume.
	sent atomic.Int64
}

func (s *sendFile) tool() agent.Tool {
	return agent.Tool{
		Name: SendFileToolName,
		Description: fmt.Sprintf("Send the person a file of this conversation, by the id %s or a tool gave: "+
			"it reaches them as a file, in this conversation, before your answer. "+
			"Use it when they want the file itself; say what is in it in your answer. At most %d a run.",
			AttachmentsToolName, filesPerRun),
		Schema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{`+
			`"id":{"type":"string","description":"the file's id"},`+
			`"caption":{"type":"string","maxLength":%d,"description":"a line sent with the file, if any"}},`+
			`"required":["id"],"additionalProperties":false}`, maxCaption)),
		Invoke: s.invoke,
	}
}

func (s *sendFile) invoke(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID      string `json:"id"`
		Caption string `json:"caption"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("the arguments are not what %s takes, an id and a caption, both strings: %w", SendFileToolName, err)
	}
	id, err := uuid.Parse(strings.TrimSpace(a.ID))
	if err != nil {
		return "", fmt.Errorf("%q is not a file's id; %s lists them", a.ID, AttachmentsToolName)
	}
	caption, err := captionOf(a.Caption)
	if err != nil {
		return "", err
	}
	call := agent.ToolCallID(ctx)
	if call == "" || len(call) > maxCallID {
		return "", errors.New("this call has no id the reply can be kept by, so the file was not sent")
	}
	row, err := s.store.GetAttachmentInSession(ctx, db.GetAttachmentInSessionParams{ID: id, SessionID: s.run.SessionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("there is no file %s in this conversation; %s lists them", id, AttachmentsToolName)
	}
	if err != nil {
		return "", fmt.Errorf("reading file %s: %w", id, err)
	}
	// A file over what the channel takes is refused before it is queued,
	// and costs no slot: it would fail the same way every time.
	if s.toChannel && row.SizeBytes > s.maxFile {
		return "", fmt.Errorf("%s is %s; files go to %s up to %s, so it was not sent. "+
			"It is in this conversation on the dashboard; say so, and what is in it",
			row.Filename, sizeOf(row.SizeBytes), channelName(s.provider), limitOf(s.maxFile))
	}
	// Counted once the file is found, so a wrong id costs no slot; a send that
	// then fails still does, so a channel that is down is not tried an
	// eleventh time.
	if s.sent.Add(1) > filesPerRun {
		return "", fmt.Errorf("this run has tried to send %d files, the most one run sends; say where the rest are instead", filesPerRun)
	}

	if s.toChannel {
		if err := s.post(ctx, call, id, caption); err != nil {
			return "", err
		}
	}
	event := stream.FileSent{
		ToolCallID: call, Caption: caption,
		File: stream.File{ID: row.ID.String(), Filename: row.Filename, MediaType: row.MediaType, Size: row.SizeBytes},
	}
	if err := s.note(ctx, "file_sent", event); err != nil {
		// On the dashboard the event is the delivery, so without it nothing
		// was sent. On a channel the reply is already queued: failing the
		// call would have the model send it again under a new call.
		if !s.toChannel {
			return "", fmt.Errorf("the file was not sent: %w", err)
		}
		s.logger.WarnContext(ctx, "file sent; its file_sent event was not recorded",
			"run", s.run.ID, "call", call, "err", err.Error())
	}
	return fmt.Sprintf("Sent %s (%s, %s) to the person.", row.Filename, row.MediaType, sizeOf(row.SizeBytes)), nil
}

// post queues the file for the run's channel and has it sent now. None
// written is the same call made again, already queued, or a channel that can
// no longer be posted to, which the model is told rather than told it went.
func (s *sendFile) post(ctx context.Context, call string, id uuid.UUID, caption string) error {
	var detail []byte
	if caption != "" {
		// A struct of one string: Marshal cannot fail.
		detail, _ = json.Marshal(struct {
			Caption string `json:"caption"`
		}{caption})
	}
	n, err := s.store.InsertFileReply(ctx, db.InsertFileReplyParams{
		RunID: s.run.ID, AttachmentID: id, CallID: call, Detail: detail,
	})
	if err != nil {
		return fmt.Errorf("the file was not sent: %w", err)
	}
	if n > 0 {
		if s.kick != nil {
			s.kick(call)
		}
		return nil
	}
	queued, err := s.store.FileReplyExists(ctx, db.FileReplyExistsParams{RunID: s.run.ID, CallID: call})
	if err != nil {
		return fmt.Errorf("the file was not sent: %w", err)
	}
	if !queued {
		return errors.New("the file was not sent: this conversation can no longer be posted to; say so in your answer")
	}
	return nil
}

// captionOf is the caption as a platform takes it: one paragraph or a few,
// no control characters but line breaks, and short enough for every one.
func captionOf(s string) (string, error) {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || !unicode.IsControl(r) && r != '\u2028' && r != '\u2029' {
			return r
		}
		return -1
	}, s))
	if n := utf8.RuneCountInString(s); n > maxCaption {
		return "", fmt.Errorf("the caption is %d characters; a file's is %d at most, so say the rest in your answer", n, maxCaption)
	}
	return s, nil
}

// sendFileFor is the tool for a run, when it can hand its person a file: a
// top-level run, as only it answers the conversation, whose conversation is
// read on its stream or whose channel posts files back. A replay holds a
// stand-in wherever a top-level run might, as it has no channel to ask.
func (b *Builder) sendFileFor(ctx context.Context, run db.Run, l Lineage) (agent.Tool, bool, error) {
	if run.ParentRunID != nil {
		return agent.Tool{}, false, nil
	}
	if b.replay {
		return (&sendFile{run: run}).tool(), true, nil
	}
	if b.sends == nil || b.sends.Store == nil || b.sends.MaxFile == nil || b.sends.Logger == nil || b.note == nil {
		return agent.Tool{}, false, nil
	}
	reach, err := b.sends.Store.GetSessionFileReach(ctx, run.SessionID)
	if err != nil {
		return agent.Tool{}, false, fmt.Errorf("reading where the conversation's files can go: %w", err)
	}
	toChannel := !reach.Streamed
	maxFile := b.sends.MaxFile(reach.Provider)
	posts := run.ReplyToChannel && reach.Postable && maxFile > 0
	if toChannel && !posts {
		return agent.Tool{}, false, nil
	}
	// A resumed run is built again: it keeps to the ten it may send.
	sent, err := b.sends.Store.CountRunFilesSent(ctx, run.ID)
	if err != nil {
		return agent.Tool{}, false, fmt.Errorf("reading how many files the run has sent: %w", err)
	}
	s := &sendFile{
		store: b.sends.Store, logger: b.sends.Logger, run: run, toChannel: toChannel, provider: reach.Provider, maxFile: maxFile,
		note: func(ctx context.Context, kind string, p any) error { return b.note(ctx, run.ID, l.Worker, kind, p) },
	}
	s.sent.Store(sent)
	if b.kickFile != nil {
		s.kick = func(call string) { b.kickFile(run.ID, call) }
	}
	return s.tool(), true, nil
}

// limitOf is a channel's limit as its platform says it: 50 MB as 50 MB, and
// a limit in MiB as sizeOf says every size.
func limitOf(n int64) string {
	if n%1_000_000 == 0 {
		return fmt.Sprintf("%d MB", n/1_000_000)
	}
	return sizeOf(n)
}

// channelName is a channel's provider as a person names it.
func channelName(provider string) string {
	switch provider {
	case "telegram":
		return "Telegram"
	case "whatsapp":
		return "WhatsApp"
	case "slack":
		return "Slack"
	}
	return "this channel"
}
