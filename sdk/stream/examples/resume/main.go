// A conversation served as a stream, a client that loses its connection
// halfway through a run's answer, and the same client picking up after the
// last event it saw. The point is the last line: nothing was missed and
// nothing came twice.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/LaplacianAI/openarity/sdk/stream"
)

func main() {
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	var log stream.Memory
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after, err := stream.Resume(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = stream.Follower{Log: &log, Poll: 10 * time.Millisecond}.Follow(w, r, after)
	}))
	defer srv.Close()

	// A person asks; a run answers in pieces, one every few milliseconds.
	log.Append("", stream.Message{ID: "m1", Parts: []stream.Part{{Type: stream.PartText, Text: "Why did the deploy fail?"}}, State: stream.StatePending})
	go func() {
		log.Append("r1", stream.RunStarted{Agent: "hub"})
		log.Append("r1", stream.MessageConsumed{MessageIDs: []string{"m1"}})
		for _, piece := range []string{"The ", "migration ", "timed ", "out."} {
			time.Sleep(20 * time.Millisecond)
			log.Append("r1", stream.TextMessageChunk{Delta: piece})
		}
		log.Append("r1", stream.RunFinished{Status: stream.StatusSucceeded, Output: "The migration timed out."})
	}()

	seen := map[int64]int{}
	last, err := read(srv.URL, "", 4, seen)
	if err != nil {
		return err
	}
	fmt.Printf("dropped   after event %d\n", last)
	if _, err := read(srv.URL, fmt.Sprint(last), 0, seen); err != nil {
		return err
	}

	twice := 0
	for _, n := range seen {
		if n > 1 {
			twice++
		}
	}
	fmt.Printf("resumed   %d events in all, %d seen twice, none missed: %v\n", len(seen), twice, len(seen) == 8)
	return nil
}

// read follows the stream, after a cursor when one is given, and prints each
// event. With stop > 0 it hangs up after that many; otherwise it reads until
// the run finishes. It returns the last sequence number it read.
func read(url, after string, stop int, seen map[int64]int) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if after != "" {
		req.Header.Set("Last-Event-ID", after)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	r := stream.NewReader(res.Body)
	for n := 1; ; n++ {
		env, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		seen[env.Seq]++
		fmt.Printf("event %d   %-20s %s\n", env.Seq, env.Event.Kind(), describe(env.Event))
		if _, done := env.Event.(stream.RunFinished); done || n == stop {
			break
		}
	}
	last, _ := r.Last()
	return last, nil
}

func describe(e stream.Event) string {
	switch e := e.(type) {
	case stream.Message:
		return fmt.Sprintf("%q (%s)", e.Parts[0].Text, e.State)
	case stream.TextMessageChunk:
		return fmt.Sprintf("%q", e.Delta)
	case stream.RunFinished:
		return fmt.Sprintf("%s: %q", e.Status, e.Output)
	}
	return ""
}
