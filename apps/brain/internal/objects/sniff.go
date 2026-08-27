package objects

import (
	"mime"
	"net/http"
)

const MaxAttachment = 25 << 20

var allowed = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"application/pdf": true,
	"application/zip": true,
	"text/plain":      true,
	"audio/mpeg":      true,
	"audio/wave":      true,
}

func Sniff(body []byte) string {
	return http.DetectContentType(body)
}

func Allowed(mediaType string) bool {
	bare, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return false
	}
	return allowed[bare]
}
