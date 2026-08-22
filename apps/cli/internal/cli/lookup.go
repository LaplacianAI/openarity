package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

const (
	lookupPageSize int32 = 100
	maxLookupPages int32 = 50
)

func ResolveTeam(ctx context.Context, api *client.ClientWithResponses, ref string) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return uuid.Nil, errors.New("name a team by name or id")
	}
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}

	limit := lookupPageSize
	var cursor *string

	for range maxLookupPages {
		page, err := Result(api.ListTeamsWithResponse(ctx,
			&client.ListTeamsParams{Limit: &limit, Cursor: cursor}))
		if err != nil {
			return uuid.Nil, fmt.Errorf("look up the team %q: %w", ref, err)
		}

		for _, team := range page.Items {
			if team.Name == ref {
				return team.ID, nil
			}
		}

		if page.NextCursor == nil {
			return uuid.Nil, fmt.Errorf(
				"no team named %q — `oa teams list` shows the ones you can see", ref)
		}
		cursor = page.NextCursor
	}

	return uuid.Nil, fmt.Errorf(
		"gave up looking for a team named %q after %d pages — pass its id instead",
		ref, maxLookupPages)
}

func ResolveMember(
	ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, ref string,
) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return uuid.Nil, errors.New("name a member by subject or id")
	}
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}

	limit := lookupPageSize
	var cursor *string

	for range maxLookupPages {
		page, err := Result(api.ListTeamMembersWithResponse(ctx, team,
			&client.ListTeamMembersParams{Limit: &limit, Cursor: cursor}))
		if err != nil {
			return uuid.Nil, fmt.Errorf("look up %q in that team: %w", ref, err)
		}

		for _, member := range page.Items {
			if member.Subject == ref {
				return member.UserID, nil
			}
		}

		if page.NextCursor == nil {
			return uuid.Nil, fmt.Errorf(
				"nobody with the subject %q is in that team — "+
					"`oa teams members list` shows who is", ref)
		}
		cursor = page.NextCursor
	}

	return uuid.Nil, fmt.Errorf(
		"gave up looking for %q after %d pages — pass their user id instead",
		ref, maxLookupPages)
}

func ResolveChannel(
	ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, ref string,
) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return uuid.Nil, errors.New("name a channel by name or id")
	}
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}

	limit := lookupPageSize
	var cursor *string

	for range maxLookupPages {
		page, err := Result(api.ListChannelsWithResponse(ctx, team,
			&client.ListChannelsParams{Limit: &limit, Cursor: cursor}))
		if err != nil {
			return uuid.Nil, fmt.Errorf("look up the channel %q: %w", ref, err)
		}

		for _, ch := range page.Items {
			if ch.Name == ref {
				return ch.ID, nil
			}
		}

		if page.NextCursor == nil {
			return uuid.Nil, fmt.Errorf(
				"no channel named %q in that team — `oa channels list` shows them", ref)
		}
		cursor = page.NextCursor
	}

	return uuid.Nil, fmt.Errorf(
		"gave up looking for a channel named %q after %d pages — pass its id instead",
		ref, maxLookupPages)
}
