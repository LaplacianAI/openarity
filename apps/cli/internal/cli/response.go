package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	maxBodyInAnError = 200
	noSuchRoute      = "404 page not found"
)

// ErrForbidden is a 403. It is a value rather than a sentence so a command
// that knows which permission its route needs can say so: the brain answers
// a refusal without naming it, and "not allowed" alone sends somebody to
// guess.
var ErrForbidden = errors.New("you are not allowed to do that")

// Needs names the permission a refused command required. Permissions are
// granted to roles per team, so the sentence says where to ask as well.
func Needs(err error, permission string) error {
	if errors.Is(err, ErrForbidden) {
		return fmt.Errorf("%w — it needs %s in that team, which a team admin can grant", err, permission)
	}
	return err
}

type (
	okResponse[T any] interface {
		GetJSON200() *T
		GetBody() []byte
		StatusCode() int
	}
	createdResponse[T any] interface {
		GetJSON201() *T
		GetBody() []byte
		StatusCode() int
	}
	emptyResponse interface {
		GetBody() []byte
		StatusCode() int
	}
)

func Result[T any](res okResponse[T], err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	if body := res.GetJSON200(); body != nil {
		return body, nil
	}
	return nil, APIError(res.StatusCode(), res.GetBody())
}

func Created[T any](res createdResponse[T], err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	if body := res.GetJSON201(); body != nil {
		return body, nil
	}
	return nil, APIError(res.StatusCode(), res.GetBody())
}

func NoContent(res emptyResponse, err error) error {
	if err != nil {
		return err
	}
	if res.StatusCode() == http.StatusNoContent {
		return nil
	}
	return APIError(res.StatusCode(), res.GetBody())
}

func APIError(status int, body []byte) error {
	detail := summarise(body)

	switch status {
	case http.StatusUnauthorized:
		return errors.New("not authenticated — run `oa login`")
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		if detail == noSuchRoute {
			return errors.New("this brain has no such endpoint — it is probably " +
				"running an older build than `oa`")
		}
		if detail != "" && detail != "not found" {
			return errors.New(detail)
		}
		return errors.New("not found, or not visible to you")
	}

	if status >= http.StatusInternalServerError {
		if detail != "" {
			return fmt.Errorf("the brain answered %d %s: %s",
				status, http.StatusText(status), detail)
		}
		return fmt.Errorf("the brain answered %d %s", status, http.StatusText(status))
	}
	if detail != "" {
		return errors.New(detail)
	}

	return fmt.Errorf("the brain answered %d %s", status, http.StatusText(status))
}

func summarise(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > maxBodyInAnError {
		return text[:maxBodyInAnError] + "…"
	}
	return text
}
