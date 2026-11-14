package runtime

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

func attachedFile(session uuid.UUID, name, mediaType string, size int64) db.Attachment {
	return db.Attachment{
		ID: uuid.New(), SessionID: session, TeamID: uuid.New(), MessageID: uuid.New(),
		ObjectKey: "k-" + name, MediaType: mediaType, SizeBytes: size, Filename: name,
		CreatedAt: time.Date(2026, 11, 14, 9, 12, 0, 0, time.UTC),
	}
}

// fileFixture is a run in a session that holds files.
func fileFixture(t *testing.T, files ...db.Attachment) (*fixture, Built) {
	t.Helper()
	f := newFixture()
	f.run.SessionID = uuid.New()
	for i := range files {
		files[i].SessionID = f.run.SessionID
	}
	f.store.attached = files
	return f, f.build(t)
}

func TestARunInAConversationWithNoFilesHasNoFileTools(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	built := f.build(t)

	for _, tl := range built.Spec.Tools {
		if tl.Name == AttachmentsToolName || tl.Name == ReadAttachmentToolName {
			t.Fatalf("%s is bound in a conversation with no files", tl.Name)
		}
	}
}

func TestARunInAConversationWithFilesCanReadThem(t *testing.T) {
	_, built := fileFixture(t, attachedFile(uuid.Nil, "screen.png", "image/png", 300<<10))

	tool(t, built, AttachmentsToolName)
	tool(t, built, ReadAttachmentToolName)
}

// The builder keeps the tool set it was given.
func TestTheBuilderIsGivenNoFileTools(t *testing.T) {
	f := newFixture()
	f.asBuilder()
	f.teamModel()
	f.run.SessionID = uuid.New()
	f.store.attached = []db.Attachment{attachedFile(f.run.SessionID, "screen.png", "image/png", 10)}

	for _, tl := range f.build(t).Spec.Tools {
		if tl.Name == ReadAttachmentToolName {
			t.Fatal("the builder can read files")
		}
	}
}

func TestAConversationsFilesThatCannotBeReadFailTheBuild(t *testing.T) {
	f := newFixture()
	down := errors.New("connection refused")
	f.store.attachedErr = down

	if err := f.buildErr(t); !errors.Is(err, down) {
		t.Fatalf("Build = %v, want %v", err, down)
	}
}

func TestAttachmentsListsTheConversationsFiles(t *testing.T) {
	f, built := fileFixture(t,
		attachedFile(uuid.Nil, "screen.png", "image/png", 300<<10),
		attachedFile(uuid.Nil, "invoice.pdf", "application/pdf", 2<<20+1<<19),
	)
	// Another conversation's file is never listed.
	f.store.attached = append(f.store.attached, attachedFile(uuid.New(), "secret.pdf", "application/pdf", 10))

	out, err := tool(t, built, AttachmentsToolName).Invoke(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "secret.pdf") {
		t.Fatalf("another conversation's file was listed: %s", out)
	}
	var got struct {
		Files []listedFile `json:"files"`
		More  bool         `json:"more"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 || got.More {
		t.Fatalf("listed %+v", got)
	}
	if g := got.Files[1]; g.Filename != "invoice.pdf" || g.MediaType != "application/pdf" || g.Size != "2.5 MB" || g.Sent != "2026-11-14T09:12:00Z" {
		t.Errorf("the PDF is listed as %+v", g)
	}
}

// Past fifty it says there are more rather than listing a long conversation.
func TestAttachmentsSaysWhenThereAreMore(t *testing.T) {
	files := make([]db.Attachment, listedFiles+1)
	for i := range files {
		files[i] = attachedFile(uuid.Nil, "f.txt", "text/plain", 1)
	}
	_, built := fileFixture(t, files...)

	out, _ := tool(t, built, AttachmentsToolName).Invoke(t.Context(), json.RawMessage(`{}`))
	if !strings.Contains(out, `"more":true`) || strings.Count(out, `"id"`) != listedFiles {
		t.Errorf("listed %d with more=%v", strings.Count(out, `"id"`), strings.Contains(out, `"more":true`))
	}
}

func readFile(t *testing.T, built Built, id string) (agent.ToolResult, error) {
	t.Helper()
	r, _, err := tool(t, built, ReadAttachmentToolName).Call(t.Context(), json.RawMessage(`{"id":"`+id+`"}`))
	return r, err
}

// A file is handed over by reference: the run's transcript keeps its name and
// never its bytes, which are not even read here.
func TestReadAttachmentHandsAFileOverByReference(t *testing.T) {
	for _, tc := range []struct {
		name, mediaType string
		size            int64
		part            agent.ContentType
	}{
		{"screen.png", "image/png", maxImageBytes, agent.ContentImage},
		{"photo.jpg", "image/jpeg; charset=binary", 10, agent.ContentImage},
		{"invoice.pdf", "application/pdf", maxDocBytes, agent.ContentFile},
		{"crash.log", "text/plain; charset=utf-8", 4 << 10, agent.ContentFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := attachedFile(uuid.Nil, tc.name, tc.mediaType, tc.size)
			f, built := fileFixture(t, file)

			r, err := readFile(t, built, file.ID.String())
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Attach) != 1 || r.Attach[0].Type != tc.part {
				t.Fatalf("attached %+v", r.Attach)
			}
			blob := r.Attach[0].Blob
			if blob.Ref != "attachment:"+file.ID.String() || len(blob.Data) != 0 || blob.Name != tc.name {
				t.Errorf("the blob is %+v", blob)
			}
			if !strings.HasPrefix(r.Output, tc.name+", ") {
				t.Errorf("output = %q", r.Output)
			}
			if len(f.objects.asked()) != 0 {
				t.Errorf("the object store was read: %v", f.objects.asked())
			}
		})
	}
}

func TestReadAttachmentRefusesWhatAModelCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, mediaType string
		size            int64
		want            string
	}{
		{"big.png", "image/png", maxImageBytes + 1, "big.png is 5.0 MB, more than the 5.0 MB an image is read up to"},
		{"big.pdf", "application/pdf", maxDocBytes + 1, "more than the 10.0 MB a PDF or a text file is read up to"},
		{"logs.zip", "application/zip", 10, "logs.zip is application/zip, which a model cannot read"},
		{"call.mp3", "audio/mpeg", 10, "which a model cannot read"},
		{"draw.svg", "image/svg+xml", 10, "which a model cannot read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := attachedFile(uuid.Nil, tc.name, tc.mediaType, tc.size)
			_, built := fileFixture(t, file)

			r, err := readFile(t, built, file.ID.String())
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(r.Attach) != 0 {
				t.Fatalf("read = %+v, %v; want %q", r, err, tc.want)
			}
		})
	}
}

// A file is found within the run's conversation, so another's id is a file
// that is not there.
func TestReadAttachmentNeverReachesAnotherConversation(t *testing.T) {
	f, built := fileFixture(t, attachedFile(uuid.Nil, "mine.png", "image/png", 10))
	theirs := attachedFile(uuid.New(), "theirs.png", "image/png", 10)
	f.store.attached = append(f.store.attached, theirs)

	r, err := readFile(t, built, theirs.ID.String())
	if err == nil || !strings.Contains(err.Error(), "no file "+theirs.ID.String()+" in this conversation") || len(r.Attach) != 0 {
		t.Fatalf("read = %+v, %v", r, err)
	}

	for _, bad := range []string{"", "a1", "../etc"} {
		if _, err := readFile(t, built, bad); err == nil || !strings.Contains(err.Error(), "not a file's id") {
			t.Errorf("read(%q) = %v", bad, err)
		}
	}
}

func TestReadAttachmentSaysWhenTheStoreFails(t *testing.T) {
	file := attachedFile(uuid.Nil, "screen.png", "image/png", 10)
	f, built := fileFixture(t, file)
	f.store.attachedErr = errors.New("connection refused")

	if _, err := readFile(t, built, file.ID.String()); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("read = %v", err)
	}
}

// An MCP server's tool under one of these names would shadow them.
func TestAServersToolNamedLikeTheFileToolsIsReported(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("odd", serve(t, AttachmentsToolName, ReadAttachmentToolName, "fetch").url, true),
	}
	if got := toolNames(f.build(t)); !slices.Equal(got, []string{"fetch"}) {
		t.Errorf("tools = %v, want fetch alone", got)
	}
}

// The first message names every file sent with it, and shows the images
// among them; a PDF or a large image is named, to be read when needed.
func TestTheFirstMessageNamesItsFilesAndShowsItsImages(t *testing.T) {
	session := uuid.New()
	shot := attachedFile(session, "screen.png", "image/png", 300<<10)
	doc := attachedFile(session, "invoice.pdf", "application/pdf", 2<<10)
	huge := attachedFile(session, "poster.png", "image/png", maxImageBytes+1)

	parts := filesOf([]db.Attachment{shot, doc, huge})
	if len(parts) != 2 {
		t.Fatalf("parts = %+v, want the note and one image", parts)
	}
	note := parts[0].Text
	for _, want := range []string{
		"screen.png (image/png, 300 KB, id " + shot.ID.String() + ")",
		"invoice.pdf (application/pdf, 2 KB, id " + doc.ID.String() + ")",
		"poster.png", ReadAttachmentToolName,
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q: %s", want, note)
		}
	}
	if parts[1].Type != agent.ContentImage || parts[1].Blob.Ref != "attachment:"+shot.ID.String() || len(parts[1].Blob.Data) != 0 {
		t.Errorf("the image is %+v", parts[1])
	}
	if filesOf(nil) != nil {
		t.Error("a message with no files says something of them")
	}
}

func TestSizesReadAsPeopleWriteThem(t *testing.T) {
	t.Parallel()

	for n, want := range map[int64]string{512: "512 bytes", 2048: "2 KB", 5 << 20: "5.0 MB", 2<<20 + 1<<19: "2.5 MB"} {
		if got := sizeOf(n); got != want {
			t.Errorf("sizeOf(%d) = %q, want %q", n, got, want)
		}
	}
}
