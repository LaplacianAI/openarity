package objects

import (
	"net/http"
	"strings"
	"testing"
)

// Byte sequences that produce each entry in the allow list. Used twice: to
// check the list has no dead entries, and as the fixtures for the tests below.
var samples = map[string][]byte{
	"image/png":       []byte("\x89PNG\r\n\x1a\n"),
	"image/jpeg":      []byte("\xff\xd8\xff"),
	"image/gif":       []byte("GIF89a"),
	"image/webp":      []byte("RIFF\x00\x00\x00\x00WEBPVP8 \x00"),
	"application/pdf": []byte("%PDF-1.7"),
	"application/zip": []byte("PK\x03\x04"),
	"text/plain":      []byte("just some words"),
	"audio/mpeg":      []byte("ID3\x03\x00"),
	"audio/wave":      []byte("RIFF\x00\x00\x00\x00WAVE\x00"),
}

// An entry nothing can sniff to is an entry that permits nothing, and it
// reads as though it permits something. The plan this came from asserted that
// Allowed("application/x-msdownload") is false — a string DetectContentType
// never returns, so the assertion held whatever the list said.
//
// This is the test that would have caught that: it runs in the other
// direction, from the list to the bytes.
func TestEveryAllowedTypeIsOneSniffCanProduce(t *testing.T) {
	t.Parallel()

	for mediaType := range allowed {
		t.Run(mediaType, func(t *testing.T) {
			t.Parallel()

			body, ok := samples[mediaType]
			if !ok {
				t.Fatalf("%q is permitted but no byte sequence here produces it — "+
					"either it is unreachable, or this test needs a sample", mediaType)
			}
			if got := Sniff(body); !strings.HasPrefix(got, mediaType) {
				t.Errorf("Sniff = %q, want it to start with %q", got, mediaType)
			}
			if !Allowed(Sniff(body)) {
				t.Errorf("Sniff produced %q, which Allowed refuses", Sniff(body))
			}
		})
	}
}

// The reason sniffing exists. A provider's declared content type is
// attacker-influenced; the bytes are not.
func TestHTMLIsNeverAnImage(t *testing.T) {
	t.Parallel()

	for name, body := range map[string][]byte{
		"plain":            []byte("<html><script>alert(1)</script></html>"),
		"leading space":    []byte("   \n<HTML><script>alert(1)</script>"),
		"uppercase":        []byte("<!DOCTYPE HTML><body>"),
		"a script element": []byte("<SCRIPT>alert(1)</SCRIPT>"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := Sniff(body)
			if strings.HasPrefix(got, "image/") {
				t.Errorf("Sniff = %q for HTML, want a text type", got)
			}
			if Allowed(got) {
				t.Errorf("Sniff = %q for HTML and Allowed said yes", got)
			}
		})
	}
}

// What a refusal actually looks like. Every one of these is a real file that
// somebody would try, and none of them sniff to the type their extension
// claims — which is why the allow list is written against what
// DetectContentType returns rather than against what the format is called.
func TestExecutablesAndMarkupAreRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		body  []byte
		sniff string
	}{
		"a Windows executable": {[]byte("MZ\x90\x00\x03"), "application/octet-stream"},
		"an ELF binary":        {[]byte("\x7fELF\x02\x01\x01\x00"), "application/octet-stream"},
		"an HTML page":         {[]byte("<html><body>hi</body></html>"), "text/html"},
		"an XML document":      {[]byte(`<?xml version="1.0"?><root/>`), "text/xml"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := Sniff(tc.body)
			if !strings.HasPrefix(got, tc.sniff) {
				t.Fatalf("Sniff = %q, want %q — this test is asserting against the "+
					"wrong string and proves nothing until it is fixed", got, tc.sniff)
			}
			if Allowed(got) {
				t.Errorf("%s sniffed as %q and was allowed", name, got)
			}
		})
	}
}

// SVG is not in Go's sniff table, so an SVG carrying a script arrives as
// text/plain — allowed, stored, and inert only for as long as it is served as
// the type it sniffed to.
//
// Pinned rather than fixed. If a future Go adds image/svg+xml this fails, and
// the allow list gets a decision rather than silently starting to refuse
// something it used to accept.
func TestSVGSniffsAsTextNotAsAnImage(t *testing.T) {
	t.Parallel()

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)

	got := Sniff(svg)
	if !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Sniff = %q for an SVG, want text/plain — Go's sniff table has "+
			"changed and the allow list needs revisiting", got)
	}
}

// The magic bytes decide, so appending markup to a real image changes
// nothing. That is safe exactly as long as the read path serves what was
// sniffed; it is stored XSS the moment anything serves by file extension.
func TestAPolyglotSniffsAsItsMagicBytes(t *testing.T) {
	t.Parallel()

	png := []byte("\x89PNG\r\n\x1a\n")
	polyglot := append(append([]byte(nil), png...), []byte("<html><script>alert(1)</script>")...)

	if got := Sniff(polyglot); !strings.HasPrefix(got, "image/png") {
		t.Errorf("Sniff = %q, want image/png", got)
	}
}

// Only the first 512 bytes are read, which is what makes sniffing cheap and
// also what bounds it: markup past that point is invisible to it.
func TestOnlyTheFirstBlockIsRead(t *testing.T) {
	t.Parallel()

	body := append([]byte("%PDF-1.7"), make([]byte, 4096)...)
	if got := Sniff(body); !strings.HasPrefix(got, "application/pdf") {
		t.Errorf("Sniff = %q for a long PDF, want application/pdf", got)
	}
}

// Allowed takes what Sniff returns, parameters and all. Comparing the raw
// string against the list would refuse every text type, since Sniff never
// returns a bare "text/plain".
func TestAllowedIgnoresTheParameters(t *testing.T) {
	t.Parallel()

	sniffed := Sniff([]byte("just some words"))
	if !strings.Contains(sniffed, ";") {
		t.Fatalf("Sniff = %q — this test exists because it carries a charset", sniffed)
	}
	if !Allowed(sniffed) {
		t.Errorf("Allowed(%q) = false, so the parameters are not being parsed off", sniffed)
	}
}

// Anything that is not a media type at all is refused rather than parsed
// loosely. This is the branch a caller reaches by passing a filename.
func TestAllowedRefusesWhatIsNotAMediaType(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"empty":           "",
		"a filename":      "holiday.png",
		"nonsense":        "not a media type at all",
		"a bad parameter": "text/plain; charset",
		"only a slash":    "/",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if Allowed(value) {
				t.Errorf("Allowed(%q) = true", value)
			}
		})
	}
}

// An empty upload sniffs as text and is therefore allowed. Recorded rather
// than guarded: whether a zero-byte attachment is worth storing is a decision
// for the route, and a media type check is the wrong place to make it.
func TestAnEmptyBodyIsTextAndIsAllowed(t *testing.T) {
	t.Parallel()

	got := Sniff(nil)
	if !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Sniff(nil) = %q, want text/plain", got)
	}
	if !Allowed(got) {
		t.Errorf("Allowed(%q) = false for an empty body", got)
	}
}

// The limit exists for two reasons and only one of them is politeness. Above
// (2^32-2)*16 bytes gcm.Seal panics rather than returning an error, so the
// constant has to stay well under it — and "well under" is what makes the
// whole-file-in-memory design tolerable at the same time.
func TestMaxAttachmentStaysUnderTheCipherCeiling(t *testing.T) {
	t.Parallel()

	const gcmCeiling = uint64((1<<32)-2) * 16

	if MaxAttachment <= 0 {
		t.Fatalf("MaxAttachment = %d", MaxAttachment)
	}
	if uint64(MaxAttachment) >= gcmCeiling {
		t.Errorf("MaxAttachment = %d, at or above the %d bytes where gcm.Seal panics",
			MaxAttachment, gcmCeiling)
	}

	// Two copies of an attachment are alive while it is sealed. A limit that
	// makes that a meaningful fraction of a container's memory is a limit
	// that has stopped doing its second job.
	if MaxAttachment > 256<<20 {
		t.Errorf("MaxAttachment = %d bytes; attachments are held whole in memory "+
			"and sealed into a second buffer, so this no longer bounds a request",
			MaxAttachment)
	}
}

// The sample table is only trustworthy if it is what the standard library
// actually says. Asserting through Sniff alone would pass if Sniff stopped
// calling DetectContentType altogether.
func TestSamplesMatchTheStandardLibrary(t *testing.T) {
	t.Parallel()

	for mediaType, body := range samples {
		if got := http.DetectContentType(body); !strings.HasPrefix(got, mediaType) {
			t.Errorf("DetectContentType(%q sample) = %q, want %q", mediaType, got, mediaType)
		}
	}
}
