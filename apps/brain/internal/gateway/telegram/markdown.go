package telegram

import "github.com/LaplacianAI/openarity/apps/brain/internal/gateway/markdown"

// richLimits is what one rich message holds: Telegram's 32768 characters and
// 500 blocks.
var richLimits = markdown.Limits{Runes: 32768, Blocks: 500}

// Sanitize makes an answer safe to send as rich Markdown. An agent's answer can
// carry text a prompt injection chose, so:
//
//   - an image becomes a link. Telegram's servers fetch an image's URL to show
//     it, which would send whatever is in the URL somewhere without a click; a
//     link waits for the reader, behind Telegram's "Open this link?".
//   - raw HTML is escaped. Rich Markdown accepts it, and a tag Telegram does not
//     know refuses the whole message, which no retry would fix.
//
// Code is left exactly as written, where both are only text.
func Sanitize(text string) string { return markdown.Rewrite(text, markdown.Images, escapeHTML) }

func escapeHTML(s string) (string, int, bool) {
	if s[0] == '<' {
		return "&lt;", 1, true
	}
	return "", 0, false
}

// Split cuts a Markdown answer into rich messages, between blocks.
func Split(text string) []string { return markdown.Split(text, richLimits) }
