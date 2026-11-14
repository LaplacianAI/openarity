package gateway

import (
	"context"
	"fmt"
	"log/slog"
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
	logger   *slog.Logger
}

func NewIngest(o Objects, r Reserver, logger *slog.Logger) *Ingest {
	return &Ingest{objects: o, reserver: r, logger: logger}
}

func (g *Ingest) Fetch(
	ctx context.Context,
	p Provider,
	req WebhookRequest,
	creds Credentials,
	teamID uuid.UUID,
	claimed []Attachment,
) []Stored {
	if len(claimed) == 0 {
		return nil
	}

	fetcher, ok := p.(Fetcher)
	if !ok {
		g.logger.Error("gateway: attachments with no fetcher",
			"provider", p.Name(), "count", len(claimed))
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, fetchBudget)
	defer cancel()

	var stored []Stored
	for _, a := range claimed {
		s, err := g.one(ctx, fetcher, req, creds, teamID, a)
		if err != nil {
			g.logger.Warn("gateway: attachment dropped",
				"provider", p.Name(), "team_id", teamID, "ref", a.Ref, "error", err)
			if ctx.Err() != nil {
				g.logger.Warn("gateway: attachment budget exhausted",
					"provider", p.Name(), "team_id", teamID, "budget", fetchBudget)
				break
			}
			continue
		}
		stored = append(stored, s)
	}
	return stored
}

func (g *Ingest) one(
	ctx context.Context,
	f Fetcher,
	req WebhookRequest,
	creds Credentials,
	teamID uuid.UUID,
	a Attachment,
) (Stored, error) {
	if a.ClaimedSize > objects.MaxAttachment {
		return Stored{}, fmt.Errorf("claims %d bytes, over the %d limit",
			a.ClaimedSize, objects.MaxAttachment)
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	body, err := f.FetchAttachment(ctx, req, a.Ref, creds)
	if err != nil {
		return Stored{}, fmt.Errorf("fetch: %w", err)
	}
	return g.Store(ctx, teamID, a.ClaimedFilename, body)
}

// Store keeps one file a person sent: its type read from its bytes, under a
// new key reserved before the bytes are written. A file that is empty, too
// large or of a type not accepted is Refused.
func (g *Ingest) Store(ctx context.Context, teamID uuid.UUID, filename string, body []byte) (Stored, error) {
	name := cleanFilename(filename)
	label := name
	if label == "" {
		label = "a file with no name"
	}
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
