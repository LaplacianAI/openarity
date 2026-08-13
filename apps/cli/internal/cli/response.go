package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const maxBodyInAnError = 200

// APIError turns a response the caller did not want into a sentence.
func APIError(res *http.Response, body []byte) error {
	detail := summarise(body)

	switch res.StatusCode {
	case http.StatusUnauthorized:
		return errors.New("not authenticated — run `oa login`")
	case http.StatusForbidden:
		return errors.New("you are not allowed to do that")
	case http.StatusNotFound:
		return errors.New("not found, or not visible to you")
	}

	if res.StatusCode >= http.StatusInternalServerError {
		if detail != "" {
			return fmt.Errorf("the brain answered %d %s: %s",
				res.StatusCode, http.StatusText(res.StatusCode), detail)
		}
		return fmt.Errorf("the brain answered %d %s",
			res.StatusCode, http.StatusText(res.StatusCode))
	}
	if detail != "" {
		return errors.New(detail)
	}

	return fmt.Errorf("the brain answered %d %s", res.StatusCode, http.StatusText(res.StatusCode))
}

func summarise(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > maxBodyInAnError {
		return text[:maxBodyInAnError] + "…"
	}
	return text
}
