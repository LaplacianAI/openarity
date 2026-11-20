package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/chromedp/cdproto"
	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"
)

const (
	dialTimeout = 10 * time.Second
	// maxMessage is the largest message the browser may send: a screenshot is
	// one, and the library's own limit of 32 KiB is smaller than any.
	maxMessage = 32 << 20
	maxVersion = 64 << 10
)

// An Allocator connects chromedp to a browser that runs already, as
// chromedp's remote allocator does, but over client: chromedp dials with a
// dialer of its own, which no guard can sit in front of.
type Allocator struct {
	url    string
	client *http.Client
	wg     sync.WaitGroup
}

// NewAllocator is a chromedp allocator context for the browser at address,
// reached over client, whose dialer decides which addresses may be reached.
func NewAllocator(parent context.Context, address string, client *http.Client) (context.Context, context.CancelFunc) {
	return chromedp.NewAllocatorContext(parent, &Allocator{url: address, client: client})
}

func (a *Allocator) Allocate(ctx context.Context, opts ...chromedp.BrowserOption) (*chromedp.Browser, error) {
	if chromedp.FromContext(ctx) == nil {
		return nil, chromedp.ErrInvalidContext
	}
	// The websocket outlives ctx until the tabs it serves are closed.
	wctx, cancel := context.WithCancel(context.Background())
	a.wg.Go(func() {
		<-ctx.Done()
		_ = chromedp.Cancel(ctx)
		cancel()
	})

	dctx, stop := context.WithTimeout(ctx, dialTimeout)
	defer stop()
	conn, err := dial(dctx, a.url, a.client)
	if err != nil {
		cancel()
		return nil, err
	}
	return chromedp.NewBrowserTransport(wctx, conn, opts...)
}

func (a *Allocator) Wait() { a.wg.Wait() }

// Attaches is true: the browser runs already, and chromedp opens a tab in it
// rather than taking one it started.
func (a *Allocator) Attaches() bool { return true }

// conn is CDP over one websocket.
type conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

// dial connects to the browser at address. An address with no path is a
// browser's root: Chrome serves its websocket at a path of its own, found at
// /json/version, while Lightpanda answers there with its root. Only the path
// is taken from what the browser says, so it cannot send the dial elsewhere.
func dial(ctx context.Context, address string, client *http.Client) (*conn, error) {
	u, err := wsURL(address)
	if err != nil {
		return nil, err
	}
	if u.Path == "" || u.Path == "/" {
		if path, ok := debuggerPath(ctx, *u, client); ok {
			u.Path = path
		}
	}
	ws, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: client})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		// The address carries the browser's key: say where, never what.
		return nil, fmt.Errorf("the browser at %s could not be reached: %w", u.Host, redact(err))
	}
	ws.SetReadLimit(maxMessage)
	return &conn{ws: ws}, nil
}

// debuggerPath is the path of the websocket the browser at u names, if it
// names one.
func debuggerPath(ctx context.Context, u url.URL, client *http.Client) (string, bool) {
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	u.Path = "/json/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req) //nolint:gosec // G704: client's dialer is the guard
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var v struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxVersion)).Decode(&v); err != nil {
		return "", false
	}
	found, err := url.Parse(v.URL)
	if err != nil || found.Path == "" || found.Path == "/" {
		return "", false
	}
	return found.Path, true
}

// redact is err without the address a url.Error names, key and all.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (c *conn) Read(ctx context.Context, msg *cdproto.Message) error {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return err
	}
	if typ != websocket.MessageText {
		return errors.New("the browser sent a binary message")
	}
	return jsonv2.Unmarshal(data, msg, chromedp.DefaultUnmarshalOptions)
}

func (c *conn) Write(ctx context.Context, msg *cdproto.Message) error {
	data, err := jsonv2.Marshal(msg, chromedp.DefaultMarshalOptions)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

func (c *conn) Close() error { return c.ws.CloseNow() }
