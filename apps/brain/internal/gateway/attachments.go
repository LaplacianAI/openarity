package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

const (
	fetchTimeout = 1 * time.Second
	fetchBudget  = 2 * time.Second
	filenameMax  = 512
)

type Objects interface {
	Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error
}

type Stored struct {
	ObjectKey string
	MediaType string
	SizeBytes int64
	Filename  string
}

type Ingest struct {
	objects Objects
	logger  *slog.Logger
}

func NewIngest(o Objects, logger *slog.Logger) *Ingest {
	return &Ingest{objects: o, logger: logger}
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
	if len(body) == 0 {
		return Stored{}, errors.New("fetched no bytes")
	}
	if len(body) > objects.MaxAttachment {
		return Stored{}, fmt.Errorf("is %d bytes, over the %d limit",
			len(body), objects.MaxAttachment)
	}

	mediaType := objects.Sniff(body)
	if !objects.Allowed(mediaType) {
		return Stored{}, fmt.Errorf("sniffed as %s, which is not accepted", mediaType)
	}

	key := objects.TeamPrefix(teamID) + "objects/" + uuid.New().String()

	if err := g.objects.Put(ctx, teamID, key, body); err != nil {
		return Stored{}, fmt.Errorf("store: %w", err)
	}

	return Stored{
		ObjectKey: key,
		MediaType: mediaType,
		SizeBytes: int64(len(body)),
		Filename:  cleanFilename(a.ClaimedFilename),
	}, nil
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
