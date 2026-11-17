package shim

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// fakeServer is a stdio MCP server, run as this test binary with
// "fake-mcp-server" as its first argument. Its tools let a test see what the
// server saw: how many times it was initialized, how many calls reached it,
// and which of its requests were cancelled.
func fakeServer() {
	f := &fake{out: bufio.NewWriter(os.Stdout), asked: map[string]chan json.RawMessage{}}
	fmt.Println("fake server: a log line on stdout, which is not JSON")

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), maxLine)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method == "" && m.ID != nil:
			f.mu.Lock()
			wait := f.asked[string(m.ID)]
			f.mu.Unlock()
			if wait != nil {
				wait <- m.Error
			}
		case m.Method == "notifications/cancelled":
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			f.mu.Lock()
			f.cancelled = append(f.cancelled, string(p.RequestID))
			f.mu.Unlock()
		case m.ID != nil:
			go f.request(m.ID, m.Method, m.Params)
		}
	}
}

type fake struct {
	wmu sync.Mutex
	out *bufio.Writer

	mu          sync.Mutex
	initializes int
	calls       int
	slow        string
	cancelled   []string
	asked       map[string]chan json.RawMessage
}

func (f *fake) send(v any) {
	line, _ := json.Marshal(v)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = f.out.Write(append(line, '\n'))
	_ = f.out.Flush()
}

func (f *fake) answer(id json.RawMessage, result any) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (f *fake) request(id json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		f.initializes++
		f.mu.Unlock()
		f.answer(id, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "fake", "version": "1"},
		})
	case "tools/list":
		var tools []map[string]any
		for _, name := range []string{"echo", "slow", "die", "ask", "seen"} {
			tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
		}
		f.answer(id, map[string]any{"tools": tools})
	case "tools/call":
		f.call(id, params)
	default:
		f.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "no " + method}})
	}
}

func (f *fake) call(id, params json.RawMessage) {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Text string `json:"text"`
			MS   int    `json:"ms"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	text := p.Arguments.Text
	switch p.Name {
	case "slow":
		f.mu.Lock()
		f.slow = string(id)
		f.mu.Unlock()
		time.Sleep(time.Duration(p.Arguments.MS) * time.Millisecond)
	case "die":
		os.Exit(3)
	case "ask":
		wait := make(chan json.RawMessage, 1)
		f.mu.Lock()
		f.asked[`"q1"`] = wait
		f.mu.Unlock()
		f.send(map[string]any{"jsonrpc": "2.0", "id": "q1", "method": "roots/list"})
		text = string(<-wait)
	case "seen":
		f.mu.Lock()
		text = fmt.Sprintf("initializes=%d calls=%d slow=%s cancelled=%s",
			f.initializes, f.calls-1, f.slow, strings.Join(f.cancelled, ","))
		f.mu.Unlock()
	}
	f.answer(id, map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
}

// seen parses what the seen tool reports.
func parseSeen(text string) map[string]string {
	out := map[string]string{}
	for field := range strings.FieldsSeq(text) {
		k, v, _ := strings.Cut(field, "=")
		out[k] = v
	}
	return out
}
