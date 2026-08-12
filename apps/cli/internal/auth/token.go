package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

var ErrNoCredential = errors.New("no credential")

type Env func(string) string

type Discoverer interface {
	GetAuthConfigWithResponse(
		ctx context.Context, reqEditors ...client.RequestEditorFn,
	) (*client.GetAuthConfigResponse, error)
}

func Resolve(ctx context.Context, api Discoverer, flagToken, savedToken string, env Env) (string, error) {
	for _, candidate := range []string{flagToken, env("OPENARITY_TOKEN"), savedToken} {
		if token := strings.TrimSpace(candidate); token != "" {
			return token, nil
		}
	}
	return developmentToken(ctx, api, env)
}

func developmentToken(ctx context.Context, api Discoverer, env Env) (string, error) {
	response, err := api.GetAuthConfigWithResponse(ctx)
	if err != nil {
		return "", fmt.Errorf("ask the server how to authenticate: %w", err)
	}
	if response.JSON200 == nil {
		return "", fmt.Errorf(
			"the server did not say how to authenticate: %s", response.HTTPResponse.Status)
	}

	if !response.JSON200.DevTokenAccepted {
		return "", fmt.Errorf("%w: run `oa login`", ErrNoCredential)
	}

	token := strings.TrimSpace(env("OPENARITY_DEV_TOKEN"))
	if token == "" {
		return "", errors.New(
			"this brain accepts a development token but OPENARITY_DEV_TOKEN is not set — " +
				"source apps/brain/.env, or run `oa login`")
	}
	return token, nil
}
