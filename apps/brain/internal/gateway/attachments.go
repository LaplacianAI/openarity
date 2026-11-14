package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	fetchTimeout = 1 * time.Second
	fetchBudget  = 2 * time.Second
	filenameMax  = 512
)

type Objects interface {
	Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error
}

// Reserver tombstones an object's key before the object is written, held
// back for an hour, so bytes whose message never commits are the reaper's:
// a delivery refused, repeated, or cut short by a crash.
type Reserver interface {
	ReserveObjects(ctx context.Context, arg db.ReserveObjectsParams) error
}

// reservationHold outlasts the slowest delivery: once it passes, a key no
// row names is deleted.
const reservationHold = time.Hour

// Refused is a file that is not stored, for a reason its sender can act on.
type Refused struct{ Reason string }

func (r *Refused) Error() string { return r.Reason }

type Stored struct {
	ObjectKey string
	MediaType string
	SizeBytes int64
	Filename  string
}

type Ingest struct {
	objects  Objects
	reserver Reserver
	// client sends a Downloader's requests; nil where nothing downloads, as
	// on the dashboard, whose files arrive in the request.
	client *http.Client
	logger *slog.Logger
}

func NewIngest(o Objects, r Reserver, client *http.Client, logger *slog.Logger) *Ingest {
	return &Ingest{objects: o, reserver: r, client: client, logger: logger}
}

// downloadSteps bounds a Downloader's chain of requests: Telegram's is two.
const downloadSteps = 4

// Fetch keeps the files a delivery named. Each one not kept is said in
// missed, a sentence naming it, for the run to tell the person.
func (g *Ingest) Fetch(
	ctx context.Context,
	p Provider,
	req WebhookRequest,
	creds Credentials,
	teamID uuid.UUID,
	claimed []Attachment,
) (stored []Stored, missed []string) {
	if len(claimed) == 0 {
		return nil, nil
	}

	fetcher, fetches := p.(Fetcher)
	downloader, downloads := p.(Downloader)
	if !fetches && !downloads {
		g.logger.Error("gateway: attachments with no fetcher",
			"provider", p.Name(), "count", len(claimed))
		return nil, nil
	}
	get := func(ctx context.Context, a Attachment) ([]byte, error) {
		if fetches {
			return fetcher.FetchAttachment(ctx, req, a.Ref, creds)
		}
		return g.download(ctx, downloader, a.Ref, creds)
	}

	perFile, total := fetchTimeout, fetchBudget
	if b, ok := p.(Budgeted); ok {
		perFile, total = b.FetchBudget()
	}
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()

	for i, a := range claimed {
		s, err := g.one(ctx, get, perFile, teamID, a)
		if err == nil {
			stored = append(stored, s)
			continue
		}
		missed = append(missed, missedOf(a, err))
		g.logger.Warn("gateway: attachment dropped",
			"provider", p.Name(), "team_id", teamID, "ref", a.Ref, "error", err)
		if ctx.Err() != nil {
			g.logger.Warn("gateway: attachment budget exhausted",
				"provider", p.Name(), "team_id", teamID, "budget", total)
			for _, rest := range claimed[i+1:] {
				missed = append(missed, labelOf(rest.ClaimedFilename)+" could not be fetched in time")
			}
			break
		}
	}
	return stored, missed
}

func (g *Ingest) one(
	ctx context.Context,
	get func(context.Context, Attachment) ([]byte, error),
	perFile time.Duration,
	teamID uuid.UUID,
	a Attachment,
) (Stored, error) {
	if a.ClaimedSize > objects.MaxAttachment {
		return Stored{}, &Refused{Reason: labelOf(a.ClaimedFilename) + " is over the 25 MB a file may be"}
	}

	ctx, cancel := context.WithTimeout(ctx, perFile)
	defer cancel()

	body, err := get(ctx, a)
	if err != nil {
		return Stored{}, fmt.Errorf("fetch: %w", err)
	}
	return g.Store(ctx, teamID, a.ClaimedFilename, body)
}

// missedOf says why a file was not kept, in words the person can act on: a
// refusal as it was given, anything else as a file that could not be fetched.
func missedOf(a Attachment, err error) string {
	var refused *Refused
	if errors.As(err, &refused) {
		return refused.Reason
	}
	return labelOf(a.ClaimedFilename) + " could not be fetched"
}

// download follows a Downloader's requests to the file. No error quotes a
// request's URL: a provider's carries its token.
func (g *Ingest) download(ctx context.Context, d Downloader, ref string, creds Credentials) ([]byte, error) {
	if g.client == nil {
		return nil, errors.New("nothing here downloads files")
	}
	var prev []byte
	for range downloadSteps {
		out, final, err := d.DownloadRequest(ref, prev, creds)
		if err != nil {
			return nil, err
		}
		body, err := g.send(ctx, out)
		if err != nil {
			return nil, err
		}
		if final {
			return body, nil
		}
		prev = body
	}
	return nil, fmt.Errorf("the download took more than %d requests", downloadSteps)
}

// send makes one request and reads at most one byte past the largest file,
// so an answer too big is refused when it is stored, never read whole.
func (g *Ingest) send(ctx context.Context, out OutboundRequest) ([]byte, error) {
	method := out.Method
	if method == "" {
		method = http.MethodPost
	}
	var body io.Reader
	if out.Body != nil {
		body = bytes.NewReader(out.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, out.URL, body)
	if err != nil {
		return nil, errors.New("the download request could not be made")
	}
	req.Header = out.Header.Clone()

	resp, err := g.client.Do(req) //nolint:gosec // G704: the client's dialer refuses what the team may not reach
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("downloading: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("the provider answered %d", resp.StatusCode)
	}
	got, err := io.ReadAll(io.LimitReader(resp.Body, objects.MaxAttachment+1))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("reading the download: %w", err)
	}
	return got, nil
}

// Store keeps one file a person sent: its type read from its bytes, under a
// new key reserved before the bytes are written. A file that is empty, too
// large or of a type not accepted is Refused.
func (g *Ingest) Store(ctx context.Context, teamID uuid.UUID, filename string, body []byte) (Stored, error) {
	name := cleanFilename(filename)
	label := labelOf(filename)
	if len(body) == 0 {
		return Stored{}, &Refused{Reason: label + " is empty"}
	}
	if len(body) > objects.MaxAttachment {
		return Stored{}, &Refused{Reason: fmt.Sprintf("%s is over the %d MB a file may be", label, objects.MaxAttachment>>20)}
	}

	mediaType := objects.Sniff(body)
	if !objects.Allowed(mediaType) {
		return Stored{}, &Refused{Reason: fmt.Sprintf("%s is %s, which is not accepted", label, mediaType)}
	}

	key := objects.TeamPrefix(teamID) + "objects/" + uuid.New().String()
	if err := g.reserver.ReserveObjects(ctx, db.ReserveObjectsParams{
		ObjectKeys: []string{key}, TeamID: teamID, HoldSeconds: reservationHold.Seconds(),
	}); err != nil {
		return Stored{}, fmt.Errorf("reserve: %w", err)
	}
	if err := g.objects.Put(ctx, teamID, key, body); err != nil {
		return Stored{}, fmt.Errorf("store: %w", err)
	}

	return Stored{ObjectKey: key, MediaType: mediaType, SizeBytes: int64(len(body)), Filename: name}, nil
}

func cleanFilename(raw string) string {
	if i := strings.LastIndexAny(raw, "/\\"); i >= 0 {
		raw = raw[i+1:]
	}

	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError:
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return ' '
		default:
			return r
		}
	}, raw)

	cleaned = strings.Join(strings.Fields(cleaned), " ")

	if utf8.RuneCountInString(cleaned) > filenameMax {
		cleaned = string([]rune(cleaned)[:filenameMax])
	}
	return cleaned
}

// labelOf is how a file is named to a person: its cleaned name, or that it
// had none.
func labelOf(filename string) string {
	if name := cleanFilename(filename); name != "" {
		return name
	}
	return "a file with no name"
}
