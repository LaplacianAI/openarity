package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// MaxKeptBytes is the most of one file a Keeper is handed. A larger one is
// named, never kept.
const MaxKeptBytes = 25 << 20

// A Keeper stores a file a tool returned, so a transcript names it rather
// than holding its bytes. With one set on a Server, every file a tool
// returns, readable or not, is handed to it, and what the model is shown of
// it carries the Kept Ref instead of the bytes.
type Keeper interface {
	Keep(ctx context.Context, f File) (Kept, error)
}

// File is one file of a tool's result.
type File struct {
	// Tool is the tool's name as the model calls it.
	Tool string
	// Name is the file's name, from its URI, cleaned of anything that could
	// end a line of the tool's text.
	Name string
	// MediaType is what the server declared, bare; it may be wrong, or "".
	MediaType string
	Data      []byte
}

// Kept is a file stored: the id the model is told it by, and the Ref a Blob
// naming it carries.
type Kept struct {
	ID, Ref string
}

// fileOf is block c, the nth that is not text, as a file to keep: false for
// a block that carries no bytes.
func fileOf(c sdk.Content, n int) (File, bool) {
	var f File
	switch c := c.(type) {
	case *sdk.ImageContent:
		f = File{Name: fmt.Sprintf("image-%d", n), MediaType: bareType(c.MIMEType), Data: c.Data}
	case *sdk.AudioContent:
		f = File{Name: fmt.Sprintf("audio-%d", n), MediaType: bareType(c.MIMEType), Data: c.Data}
	case *sdk.EmbeddedResource:
		if c.Resource == nil {
			return File{}, false
		}
		f = File{Name: nameOf(c.Resource.URI, n), MediaType: bareType(c.Resource.MIMEType), Data: c.Resource.Blob}
		if len(f.Data) == 0 {
			f.Data = []byte(c.Resource.Text)
		}
	default:
		return File{}, false
	}
	if len(f.Data) == 0 {
		return File{}, false
	}
	if ext := images[kindOf(f.MediaType)]; ext != "" {
		f.Name = withExtension(f.Name, ext)
	}
	return f, true
}

// kept is block c, the nth that is not text, kept by keep and named by its
// id: its line, and the part the model is shown it as, which holds the
// Kept Ref and never the bytes. files is how many the call has kept.
func kept(ctx context.Context, keep Keeper, f File, c sdk.Content, n int, files *int) (string, *agent.Content) {
	label := fmt.Sprintf("%s: %s, %s", f.Name, kindOf(f.MediaType), sizeOf(len(f.Data)))
	switch {
	case *files >= FilesPerCall:
		return fmt.Sprintf("[%s, past the %d files a call passes on]", label, FilesPerCall), nil
	case len(f.Data) > MaxKeptBytes:
		return fmt.Sprintf("[%s, more than the %s a tool's file is kept up to]", label, sizeOf(MaxKeptBytes)), nil
	}
	k, err := keep.Keep(ctx, f)
	if err != nil {
		// The keeper's own error is the caller's to log: it may name a store.
		return "[" + label + ", which could not be kept]", nil
	}
	*files++

	shown, note := fileIn(c, n)
	id := ", file " + k.ID
	switch {
	case shown == nil:
		return withID(note, id), nil
	case shown.text != "":
		return withID(shown.text, id), nil
	}
	part := shown.Content
	blob := *part.Blob
	blob.Data, blob.Ref = nil, k.Ref
	part.Blob = &blob
	return withID(note, id), &part
}

// withID is a file's line with its id added to the bracket that opens it. A
// name and a type are cleaned of brackets, so the first one closing is that
// bracket's.
func withID(line, id string) string {
	i := strings.Index(line, "]")
	if i < 0 {
		return line
	}
	return line[:i] + id + line[i:]
}
