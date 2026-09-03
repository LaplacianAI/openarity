package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// tool is one tool a test server offers: what it declares, and what it answers.
type tool struct {
	name    string
	desc    string
	schema  any
	handler sdk.ToolHandler
}

// text is the handler most tests want — one text block, ignoring the arguments.
func text(s string) sdk.ToolHandler {
	return func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}, nil
	}
}

// serve stands up a real MCP server over streamable HTTP and returns its URL.
// A real server rather than a hand-written fake, because the thing under test
// is a protocol conversation: a fake would agree with whatever this package
// happens to send.
func serve(t *testing.T, tools ...tool) string {
	t.Helper()

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "v1"}, nil)
	for _, tl := range tools {
		schema := tl.schema
		if schema == nil {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		server.AddTool(&sdk.Tool{
			Name:        tl.name,
			Description: tl.desc,
			InputSchema: schema,
		}, tl.handler)
	}

	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

// connect opens a session against a test server and closes it with the test.
func connect(t *testing.T, s Server) *Session {
	t.Helper()

	session, err := Connect(t.Context(), s)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func names(session *Session) []string {
	out := make([]string, 0, len(session.Tools()))
	for _, tl := range session.Tools() {
		out = append(out, tl.Name)
	}
	return out
}

func find(t *testing.T, session *Session, name string) func(context.Context, json.RawMessage) (string, error) {
	t.Helper()

	for _, tl := range session.Tools() {
		if tl.Name == name {
			return tl.Invoke
		}
	}
	t.Fatalf("no tool named %q; the session offered %v", name, names(session))
	return nil
}

// A server's name prefixes its tools, because the names come from somebody
// else and two servers calling something "search" is the normal case.
func TestToolsArriveNamespacedByTheirServer(t *testing.T) {
	t.Parallel()

	url := serve(t,
		tool{name: "search", handler: text("found")},
		tool{name: "create_issue", handler: text("created")},
	)

	session := connect(t, Server{Name: "github", URL: url})

	got := names(session)
	want := map[string]bool{"github__search": true, "github__create_issue": true}
	if len(got) != len(want) {
		t.Fatalf("offered %v, want %d tools", got, len(want))
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("offered %q, which is not namespaced by its server", name)
		}
	}
}

func TestBareOffersTheServersOwnNames(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{name: "search", handler: text("found")})
	session := connect(t, Server{Name: "github", URL: url, Bare: true})

	if got := names(session); len(got) != 1 || got[0] != "search" {
		t.Errorf("offered %v, want [search]", got)
	}
}

// The gateway would refuse this on the first call, naming neither the server
// nor the tool. Refusing at connect is the difference between a debuggable
// error and a puzzling one.
func TestANameNoProviderWouldAcceptIsRefusedAtConnect(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 60)
	url := serve(t, tool{name: long, handler: text("ok")})

	_, err := Connect(t.Context(), Server{Name: "github", URL: url})
	if err == nil {
		t.Fatal("Connect accepted a tool whose prefixed name is 67 characters")
	}
	for _, want := range []string{"github", long} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// Bare is the escape hatch, and the same 64-character ceiling still applies to
// what comes back — a long name is not made legal by declining to prefix it.
func TestBareStillRefusesAnUnusableName(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{name: strings.Repeat("b", 65), handler: text("ok")})

	if _, err := Connect(t.Context(), Server{Name: "github", URL: url, Bare: true}); err == nil {
		t.Fatal("Connect accepted a 65-character tool name")
	}
}

func TestTheServersSchemaTravelsAsGiven(t *testing.T) {
	t.Parallel()

	declared := json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}},"required":["repo"]}`)
	url := serve(t, tool{name: "search", schema: declared, handler: text("found")})

	session := connect(t, Server{Name: "github", URL: url})

	var got map[string]any
	if err := json.Unmarshal(session.Tools()[0].Schema, &got); err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}

	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the schema lost its properties: %s", session.Tools()[0].Schema)
	}
	if _, ok := props["repo"]; !ok {
		t.Errorf("the schema lost the repo property: %s", session.Tools()[0].Schema)
	}
	if req, ok := got["required"].([]any); !ok || len(req) != 1 {
		t.Errorf("the schema lost its required list: %s", session.Tools()[0].Schema)
	}
}

func TestADescriptionTravels(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{name: "search", desc: "Search the issues.", handler: text("found")})
	session := connect(t, Server{Name: "github", URL: url})

	if got := session.Tools()[0].Description; got != "Search the issues." {
		t.Errorf("Description = %q, want the server's", got)
	}
}

func TestAToolsTextReachesTheCaller(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{name: "search", handler: text("three open issues")})
	session := connect(t, Server{Name: "github", URL: url})

	out, err := find(t, session, "github__search")(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out != "three open issues" {
		t.Errorf("Invoke = %q, want the server's text", out)
	}
}

// The arguments the model produced have to arrive as the server declared them,
// or every tool call is a silent no-op with the fields zeroed.
func TestTheArgumentsReachTheServer(t *testing.T) {
	t.Parallel()

	var seen string
	url := serve(t, tool{
		name: "search",
		handler: func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			seen = string(req.Params.Arguments)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		},
	})

	session := connect(t, Server{Name: "github", URL: url})
	if _, err := find(t, session, "github__search")(t.Context(), json.RawMessage(`{"repo":"openarity"}`)); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if !strings.Contains(seen, `"repo"`) || !strings.Contains(seen, `"openarity"`) {
		t.Errorf("the server saw %s, which is not what was sent", seen)
	}
}

// A tool that takes no arguments is called with an object, not with null.
// json.Unmarshal accepts null silently, so the mistake would look like a tool
// that runs with every field empty.
func TestNoArgumentsBecomeAnEmptyObject(t *testing.T) {
	t.Parallel()

	var seen string
	url := serve(t, tool{
		name: "now",
		handler: func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			seen = string(req.Params.Arguments)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		},
	})

	session := connect(t, Server{Name: "clock", URL: url})
	if _, err := find(t, session, "clock__now")(t.Context(), nil); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if seen == "null" {
		t.Errorf("the server was sent null rather than an object")
	}
}

func TestArgumentsThatAreNotAnObjectAreRefused(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{name: "search", handler: text("found")})
	session := connect(t, Server{Name: "github", URL: url})

	_, err := find(t, session, "github__search")(t.Context(), json.RawMessage(`"not an object"`))
	if err == nil {
		t.Fatal("Invoke accepted arguments that are not a JSON object")
	}
}

// A tool reporting failure is not the run failing. The text comes back as the
// error so the loop puts it in the conversation and the model can correct
// itself — which is the whole reason MCP has IsError rather than a transport
// error for this.
func TestAToolsOwnErrorComesBackAsText(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{
		name: "search",
		handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{
				IsError: true,
				Content: []sdk.Content{&sdk.TextContent{Text: "repository not found"}},
			}, nil
		},
	})

	session := connect(t, Server{Name: "github", URL: url})
	_, err := find(t, session, "github__search")(t.Context(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("a tool that reported an error was treated as success")
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("the tool's own message was lost: %v", err)
	}
}

func TestAToolThatFailsAndSaysNothingStillSaysSomething(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{
		name: "search",
		handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{IsError: true}, nil
		},
	})

	session := connect(t, Server{Name: "github", URL: url})
	_, err := find(t, session, "github__search")(t.Context(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("a tool that reported an error was treated as success")
	}
	if err.Error() == "" {
		t.Error("the error is empty, so the model would see nothing")
	}
}

// An empty string reads to a model as a tool that does nothing, and it will
// call it again. Saying so is not decoration.
func TestASilentSuccessSaysItReturnedNothing(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{
		name: "ping",
		handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{}, nil
		},
	})

	session := connect(t, Server{Name: "net", URL: url})
	out, err := find(t, session, "net__ping")(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out == "" {
		t.Error("an empty answer reached the model as an empty string")
	}
}

func TestNonTextContentIsCountedRatherThanDropped(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{
		name: "screenshot",
		handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{
				&sdk.TextContent{Text: "here it is"},
				&sdk.ImageContent{Data: []byte{1, 2, 3}, MIMEType: "image/png"},
			}}, nil
		},
	})

	session := connect(t, Server{Name: "browser", URL: url})
	out, err := find(t, session, "browser__screenshot")(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(out, "here it is") {
		t.Errorf("the text block was lost: %q", out)
	}
	if !strings.Contains(out, "omitted") {
		t.Errorf("the image was dropped without saying so: %q", out)
	}
}

func TestSeveralTextBlocksAreJoined(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{
		name: "read",
		handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{
				&sdk.TextContent{Text: "first"},
				&sdk.TextContent{Text: "second"},
			}}, nil
		},
	})

	session := connect(t, Server{Name: "fs", URL: url})
	out, err := find(t, session, "fs__read")(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out != "first\nsecond" {
		t.Errorf("Invoke = %q, want both blocks joined", out)
	}
}

// A server with more tools than fit in one page is ordinary. Stopping at the
// first page loses the rest and looks like a server that offers less.
func TestEveryPageOfToolsIsRead(t *testing.T) {
	t.Parallel()

	const many = 60

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "v1"}, nil)
	for i := range many {
		server.AddTool(&sdk.Tool{
			Name:        "tool_" + string(rune('a'+i/26)) + string(rune('a'+i%26)),
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}, text("ok"))
	}

	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	session := connect(t, Server{Name: "big", URL: httpServer.URL})
	if got := len(session.Tools()); got != many {
		t.Errorf("read %d tools, want %d — a page was dropped", got, many)
	}
}

func TestAServerWithNoNameIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := Connect(t.Context(), Server{URL: "http://example.invalid"}); err == nil {
		t.Fatal("Connect accepted a server with no name")
	}
}

func TestNeitherCommandNorURLIsRefused(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), Server{Name: "github"})
	if err == nil {
		t.Fatal("Connect accepted a server with no transport")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
}

func TestBothCommandAndURLIsRefused(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), Server{
		Name:    "github",
		Command: []string{"true"},
		URL:     "http://example.invalid",
	})
	if err == nil {
		t.Fatal("Connect accepted a server with two transports")
	}
}

func TestAnUnreachableServerNamesItself(t *testing.T) {
	t.Parallel()

	// A port nothing listens on. Connect has to fail rather than hand back a
	// session whose every tool call fails later.
	_, err := Connect(t.Context(), Server{Name: "github", URL: "http://127.0.0.1:1/mcp"})
	if err == nil {
		t.Fatal("Connect succeeded against a closed port")
	}
	if !strings.Contains(err.Error(), "github") {
		t.Errorf("the error does not name the server: %v", err)
	}
}

func TestGatherRefusesTwoServersClaimingOneName(t *testing.T) {
	t.Parallel()

	first := connect(t, Server{Name: "a", URL: serve(t, tool{name: "search", handler: text("x")}), Bare: true})
	second := connect(t, Server{Name: "b", URL: serve(t, tool{name: "search", handler: text("y")}), Bare: true})

	_, err := Gather(first, second)
	if err == nil {
		t.Fatal("Gather accepted two servers offering the same tool name")
	}
	for _, want := range []string{"a", "b", "search"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

func TestGatherKeepsEveryToolWhenNamesDiffer(t *testing.T) {
	t.Parallel()

	first := connect(t, Server{Name: "github", URL: serve(t, tool{name: "search", handler: text("x")})})
	second := connect(t, Server{Name: "jira", URL: serve(t, tool{name: "search", handler: text("y")})})

	tools, err := Gather(first, second)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("Gather returned %d tools, want 2", len(tools))
	}
}

func TestGatherOfNothingIsEmptyRatherThanAnError(t *testing.T) {
	t.Parallel()

	tools, err := Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("Gather() returned %d tools", len(tools))
	}
}

func TestNameIsTheNameItWasGiven(t *testing.T) {
	t.Parallel()

	session := connect(t, Server{Name: "github", URL: serve(t, tool{name: "search", handler: text("x")})})
	if got := session.Name(); got != "github" {
		t.Errorf("Name() = %q, want github", got)
	}
}

// After Close, the connection is gone and a call has to fail rather than hang
// or answer from somewhere.
func TestCallingAfterCloseFails(t *testing.T) {
	t.Parallel()

	url := serve(t, tool{name: "search", handler: text("found")})
	session, err := Connect(t.Context(), Server{Name: "github", URL: url})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	invoke := find(t, session, "github__search")
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := invoke(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Error("a tool call succeeded after the session was closed")
	}
}

// The stdio path, over a real child process. This is the transport the
// security note is about, so it is worth the re-exec rather than trusting the
// HTTP tests to cover the same code.
func TestAStdioServerIsReachedOverAChildProcess(t *testing.T) {
	t.Parallel()

	session := connect(t, Server{
		Name:    "child",
		Command: []string{os.Args[0], "-test.run=TestHelperServer"},
		Env:     []string{"MCP_HELPER=1"},
	})

	out, err := find(t, session, "child__echo")(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(out, "from the child") {
		t.Errorf("Invoke = %q, want the child's answer", out)
	}
}

// exec gives a nil Env the parent's whole environment, which would hand a
// third-party server every credential this process holds. The child reports
// what it can see, and it must not see this.
func TestAChildDoesNotInheritTheParentsEnvironment(t *testing.T) {
	t.Setenv("OPENARITY_SECRET_UNDER_TEST", "leaked")

	session := connect(t, Server{
		Name:    "child",
		Command: []string{os.Args[0], "-test.run=TestHelperServer"},
		Env:     []string{"MCP_HELPER=1"},
	})

	out, err := find(t, session, "child__env")(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if strings.Contains(out, "leaked") {
		t.Errorf("the child inherited the parent's environment: %q", out)
	}
}

// TestHelperServer is not a test. Re-executed as a child process by the two
// tests above, it serves MCP over stdin and stdout; run directly it does
// nothing, so `go test` sees a test that passes.
func TestHelperServer(t *testing.T) {
	if os.Getenv("MCP_HELPER") != "1" {
		t.Skip("run only as a child process")
	}

	server := sdk.NewServer(&sdk.Implementation{Name: "helper", Version: "v1"}, nil)
	server.AddTool(&sdk.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
		text("from the child"))
	server.AddTool(&sdk.Tool{Name: "env", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{
				&sdk.TextContent{Text: strings.Join(os.Environ(), "\n")},
			}}, nil
		})

	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		t.Fatalf("the helper server stopped: %v", err)
	}
}

// A command that is not an MCP server at all. The transport starts, the
// handshake fails, and Connect has to say so rather than returning a session
// with no tools.
func TestACommandThatIsNotAServerIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no true(1) on this machine")
	}

	if _, err := Connect(t.Context(), Server{Name: "broken", Command: []string{"true"}}); err == nil {
		t.Fatal("Connect succeeded against a command that speaks no MCP")
	}
}

// The tests below need a server that misbehaves in ways the SDK's own server
// will not: a tool with no schema, a page that fails, two tools with one name.
// So this one is raw JSON-RPC over stdin and stdout, answering with exactly
// the bytes each case needs.

type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string `json:"protocolVersion"`
		Cursor          string `json:"cursor"`
	} `json:"params"`
}

// TestHelperRawServer is not a test. MCP_RAW selects which malformation to
// exhibit; run directly it does nothing.
func TestHelperRawServer(t *testing.T) {
	quirk := os.Getenv("MCP_RAW")
	if quirk == "" {
		t.Skip("run only as a child process")
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)

	reply := func(id json.RawMessage, result string) {
		fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", id, result)
		_ = out.Flush()
	}

	pages := 0
	for in.Scan() {
		var req request
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			// Echo the version the client asked for; disagreeing here ends the
			// conversation before the case under test is reached.
			reply(req.ID, fmt.Sprintf(
				`{"protocolVersion":%q,"capabilities":{"tools":{}},"serverInfo":{"name":"raw","version":"v1"}}`,
				req.Params.ProtocolVersion))

		case "tools/list":
			pages++
			reply(req.ID, listing(quirk, pages))

		case "tools/call":
			reply(req.ID, `{"content":[{"type":"text","text":"ok"}]}`)

		default:
			// Anything else gets method-not-found. The client opens with
			// server/discover from protocol 2026-07-28 and falls back to
			// initialize when that is refused; staying silent instead leaves
			// it waiting for a reply that never comes.
			if len(req.ID) > 0 {
				fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"no such method"}}`+"\n", req.ID)
				_ = out.Flush()
			}
		}
	}
}

func listing(quirk string, page int) string {
	switch quirk {
	case "noschema":
		// inputSchema omitted entirely, which the spec allows a client to meet.
		return `{"tools":[{"name":"bare","description":"no schema at all"}]}`

	case "dupes":
		return `{"tools":[
			{"name":"same","inputSchema":{"type":"object"}},
			{"name":"same","inputSchema":{"type":"object"}}
		]}`

	case "badpage":
		if page == 1 {
			return `{"tools":[{"name":"first","inputSchema":{"type":"object"}}],"nextCursor":"more"}`
		}
		// A second page that is not a listing at all. The client asked for
		// more and got something it cannot read.
		return `{"tools":"not an array"}`
	}
	return `{"tools":[]}`
}

func raw(t *testing.T, quirk string) Server {
	t.Helper()
	return Server{
		Name:    "raw",
		Command: []string{os.Args[0], "-test.run=TestHelperRawServer"},
		Env:     []string{"MCP_RAW=" + quirk},
	}
}

// A server may leave inputSchema out. An empty Schema would reach the gateway
// as a tool with no parameters block, which some refuse outright.
func TestAToolWithNoSchemaGetsAnObjectOne(t *testing.T) {
	t.Parallel()

	session := connect(t, raw(t, "noschema"))
	if len(session.Tools()) != 1 {
		t.Fatalf("offered %v, want one tool", names(session))
	}

	var got map[string]any
	if err := json.Unmarshal(session.Tools()[0].Schema, &got); err != nil {
		t.Fatalf("the schema is not JSON: %q", session.Tools()[0].Schema)
	}
	if got["type"] != "object" {
		t.Errorf("Schema = %q, want an object schema", session.Tools()[0].Schema)
	}
}

// One server offering the same name twice would otherwise silently lose one,
// and which one it lost would depend on map ordering.
func TestAServerOfferingOneNameTwiceIsRefused(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), raw(t, "dupes"))
	if err == nil {
		t.Fatal("Connect accepted a server offering the same tool twice")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

// The failure has to surface. A pagination loop that gave up quietly would
// return the first page and look like a smaller server.
func TestAPageThatFailsIsNotSilentlyTruncated(t *testing.T) {
	t.Parallel()

	_, err := Connect(t.Context(), raw(t, "badpage"))
	if err == nil {
		t.Fatal("Connect returned a truncated tool list as though it were whole")
	}
	if !strings.Contains(err.Error(), "raw") {
		t.Errorf("the error does not name the server: %v", err)
	}
}
