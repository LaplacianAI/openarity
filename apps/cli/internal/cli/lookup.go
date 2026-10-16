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

// byName walks a paged list until an item's name matches. Every resolver
// below is this loop with a different list, so a skill, an agent and a server
// cannot drift apart in how they look one up or in what they say on a miss.
func byName[T any](
	ctx context.Context, ref, kind, hint string,
	list func(ctx context.Context, limit int32, cursor *string) ([]T, *string, error),
	name func(T) string, id func(T) uuid.UUID,
) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return uuid.Nil, fmt.Errorf("name a %s by name or id", kind)
	}
	if parsed, err := uuid.Parse(ref); err == nil {
		return parsed, nil
	}

	var cursor *string
	for range maxLookupPages {
		items, next, err := list(ctx, lookupPageSize, cursor)
		if err != nil {
			return uuid.Nil, fmt.Errorf("look up the %s %q: %w", kind, ref, err)
		}

		for _, item := range items {
			if name(item) == ref {
				return id(item), nil
			}
		}

		if next == nil {
			return uuid.Nil, fmt.Errorf("no %s named %q in that team — `%s` shows them", kind, ref, hint)
		}
		cursor = next
	}

	return uuid.Nil, fmt.Errorf(
		"gave up looking for a %s named %q after %d pages — pass its id instead",
		kind, ref, maxLookupPages)
}

func ResolveSkill(
	ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, ref string,
) (uuid.UUID, error) {
	return byName(ctx, ref, "skill", "oa skills list",
		func(ctx context.Context, limit int32, cursor *string) ([]client.SkillSummary, *string, error) {
			page, err := Result(api.ListSkillsWithResponse(ctx, team,
				&client.ListSkillsParams{Limit: &limit, Cursor: cursor}))
			if err != nil {
				return nil, nil, err
			}
			return page.Items, page.NextCursor, nil
		},
		func(s client.SkillSummary) string { return s.Name },
		func(s client.SkillSummary) uuid.UUID { return s.ID })
}

func ResolveAgent(
	ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, ref string,
) (uuid.UUID, error) {
	return byName(ctx, ref, "agent", "oa agents list",
		func(ctx context.Context, limit int32, cursor *string) ([]client.AgentSummary, *string, error) {
			page, err := Result(api.ListAgentsWithResponse(ctx, team,
				&client.ListAgentsParams{Limit: &limit, Cursor: cursor}))
			if err != nil {
				return nil, nil, err
			}
			return page.Items, page.NextCursor, nil
		},
		func(a client.AgentSummary) string { return a.Name },
		func(a client.AgentSummary) uuid.UUID { return a.ID })
}

func ResolveMCPServer(
	ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, ref string,
) (uuid.UUID, error) {
	return byName(ctx, ref, "MCP server", "oa mcp-servers list",
		func(ctx context.Context, limit int32, cursor *string) ([]client.MCPServer, *string, error) {
			page, err := Result(api.ListMCPServersWithResponse(ctx, team,
				&client.ListMCPServersParams{Limit: &limit, Cursor: cursor}))
			if err != nil {
				return nil, nil, err
			}
			return page.Items, page.NextCursor, nil
		},
		func(s client.MCPServer) string { return s.Name },
		func(s client.MCPServer) uuid.UUID { return s.ID })
}
