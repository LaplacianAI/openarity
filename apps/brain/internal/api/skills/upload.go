package skills

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
)

const maxUploadBytes = skill.MaxSkillBytes + 1<<20

const filesField = "files"

func readUpload(w http.ResponseWriter, r *http.Request) (skill.Directory, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)

	var entries []skill.Entry
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case err == nil && mediaType == "application/zip":
		entries, err = readZip(r.Body)
	case err == nil && mediaType == "multipart/form-data":
		entries, err = readParts(multipart.NewReader(r.Body, params["boundary"]))
	default:
		http.Error(w, "send the skill as multipart/form-data or application/zip", http.StatusUnsupportedMediaType)
		return skill.Directory{}, false
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, fmt.Sprintf("an upload is at most %d MiB", maxUploadBytes>>20), http.StatusRequestEntityTooLarge)
		return skill.Directory{}, false
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return skill.Directory{}, false
	}

	dir, err := skill.Assemble(entries)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return skill.Directory{}, false
	}
	return dir, true
}

func readZip(body io.Reader) ([]skill.Entry, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	return skill.ReadZip(data)
}

func readParts(mr *multipart.Reader) ([]skill.Entry, error) {
	var c skill.Collector
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return c.Entries(), nil
		}
		if err != nil {
			return nil, fmt.Errorf("the upload is not valid multipart: %w", err)
		}

		path, err := partPath(p)
		if err != nil {
			return nil, err
		}
		if err := c.Add(path, p); err != nil {
			return nil, err
		}
	}
}

func partPath(p *multipart.Part) (string, error) {
	_, params, err := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
	path, named := params["filename"]
	if err != nil || p.FormName() != filesField || !named {
		return "", fmt.Errorf("every part must be a file in the %q field, its path as the filename", filesField)
	}
	return path, nil
}
