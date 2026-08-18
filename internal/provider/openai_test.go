package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEScanner(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "basic events",
			in:   "data: one\n\ndata: two\n\n",
			want: []string{"one", "two"},
		},
		{
			name: "crlf line endings",
			in:   "data: one\r\n\r\ndata: two\r\n\r\n",
			want: []string{"one", "two"},
		},
		{
			name: "no space after colon",
			in:   "data:one\n\n",
			want: []string{"one"},
		},
		{
			name: "comments and keep-alives skipped",
			in:   ": ping\n\n\ndata: one\n\n",
			want: []string{"one"},
		},
		{
			name: "multi-line data joined",
			in:   "data: line1\ndata: line2\n\n",
			want: []string{"line1\nline2"},
		},
		{
			name: "final event without trailing blank line",
			in:   "data: one\n\ndata: two",
			want: []string{"one", "two"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := newSSEScanner(strings.NewReader(tt.in))
			var got []string
			for {
				data, err := sc.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("Next: %v", err)
				}
				got = append(got, string(data))
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d events %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("event %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestToolCallAccumulator(t *testing.T) {
	acc := toolCallAccumulator{}
	// Fragments arrive index-keyed: first carries id+name, rest append args.
	acc.add(chunkToolCall{Index: 0, ID: "call_a", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "read_file", Arguments: `{"pa`}})
	acc.add(chunkToolCall{Index: 1, ID: "call_b", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "grep", Arguments: `{"pattern":"x"}`}})
	acc.add(chunkToolCall{Index: 0, Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Arguments: `th":"a.go"}`}})

	calls := acc.calls()
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].ID != "call_a" || calls[0].Name != "read_file" || calls[0].Args != `{"path":"a.go"}` {
		t.Errorf("call 0 = %+v", calls[0])
	}
	if calls[1].ID != "call_b" || calls[1].Name != "grep" {
		t.Errorf("call 1 = %+v", calls[1])
	}
}

// serveSSE returns a test server that writes the given SSE body for any
// chat-completions request.
func serveSSE(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
}

func collect(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var evs []Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestStreamTextAndUsage(t *testing.T) {
	srv := serveSSE(t, ""+
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`+"\n\n"+
		`data: {"choices":[{"delta":{"content":"lo"}}]}`+"\n\n"+
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n"+
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":8}}}`+"\n\n"+
		"data: [DONE]\n\n")
	defer srv.Close()

	p := NewOpenAICompat(srv.URL+"/v1", "")
	ch, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	var text string
	var usage *Usage
	var done bool
	for _, ev := range evs {
		switch ev.Kind {
		case EventTextDelta:
			text += ev.Text
		case EventUsage:
			usage = ev.Usage
		case EventDone:
			done = true
			if ev.FinishReason != "stop" {
				t.Errorf("finish = %q, want stop", ev.FinishReason)
			}
		case EventError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if text != "Hello" {
		t.Errorf("text = %q, want Hello", text)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CachedTokens != 8 {
		t.Errorf("usage = %+v", usage)
	}
	if !done {
		t.Error("no EventDone")
	}
}

func TestStreamToolCallFragments(t *testing.T) {
	srv := serveSSE(t, ""+
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":""}}]}}]}`+"\n\n"+
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`+"\n\n"+
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`+"\n\n"+
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n"+
		"data: [DONE]\n\n")
	defer srv.Close()

	p := NewOpenAICompat(srv.URL+"/v1", "")
	ch, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	var calls []ToolCall
	finish := ""
	for _, ev := range evs {
		switch ev.Kind {
		case EventToolCalls:
			calls = ev.ToolCalls
		case EventDone:
			finish = ev.FinishReason
		case EventError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "read_file" || calls[0].Args != `{"path":"a.go"}` {
		t.Errorf("call = %+v", calls[0])
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q, want tool_calls", finish)
	}
}

func TestStreamThinkingDelta(t *testing.T) {
	srv := serveSSE(t, ""+
		`data: {"choices":[{"delta":{"reasoning_content":"hmm"}}]}`+"\n\n"+
		`data: {"choices":[{"delta":{"content":"answer"}}]}`+"\n\n"+
		"data: [DONE]\n\n")
	defer srv.Close()

	p := NewOpenAICompat(srv.URL+"/v1", "")
	ch, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var thinking, text string
	for ev := range ch {
		switch ev.Kind {
		case EventThinkingDelta:
			thinking += ev.Text
		case EventTextDelta:
			text += ev.Text
		}
	}
	if thinking != "hmm" || text != "answer" {
		t.Errorf("thinking = %q text = %q", thinking, text)
	}
}

func TestStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL+"/v1", "")
	_, err := p.Stream(context.Background(), Request{Model: "nope"})
	if err == nil {
		t.Fatal("want error for 404")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error should carry body, got: %v", err)
	}
}

func TestStreamRetriesOn500(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL+"/v1", "")
	ch, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	var text string
	for ev := range ch {
		if ev.Kind == EventTextDelta {
			text += ev.Text
		}
	}
	if text != "ok" || attempts != 3 {
		t.Errorf("text = %q attempts = %d", text, attempts)
	}
}

func TestStreamCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"start"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-release // hold the stream open
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	p := NewOpenAICompat(srv.URL+"/v1", "")
	ch, err := p.Stream(ctx, Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	// Read the first delta, then cancel mid-stream.
	<-ch
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return // channel closed: goroutine exited, no leak
			}
			if ev.Kind == EventError && !errors.Is(ev.Err, context.Canceled) {
				t.Errorf("error = %v, want context.Canceled", ev.Err)
			}
		case <-deadline:
			t.Fatal("stream did not terminate after cancel")
		}
	}
}

func TestToWireImageParts(t *testing.T) {
	req := Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleUser, Content: "plain"},
			{Role: RoleUser, Content: "what is this", Images: []string{"data:image/png;base64,AAAA"}},
			{Role: RoleUser, Images: []string{"data:image/jpeg;base64,BBBB"}},
		},
	}

	// Vision-capable adapter: images become content parts.
	w := toWire(NewOpenAICompatVision("http://localhost:11434/v1", ""), req)

	if got, ok := w.Messages[0].Content.(string); !ok || got != "plain" {
		t.Fatalf("message without images should stay a string, got %#v", w.Messages[0].Content)
	}

	parts, ok := w.Messages[1].Content.([]wirePart)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected 2 content parts, got %#v", w.Messages[1].Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "what is this" {
		t.Fatalf("first part should be the text, got %#v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "data:image/png;base64,AAAA" {
		t.Fatalf("second part should be the image, got %#v", parts[1])
	}

	parts, ok = w.Messages[2].Content.([]wirePart)
	if !ok || len(parts) != 1 || parts[0].Type != "image_url" {
		t.Fatalf("image-only message should have one image part, got %#v", w.Messages[2].Content)
	}

	// Text-only adapter (DeepSeek and friends): images never reach the
	// wire; the provider would reject the part outright. The message
	// keeps its plain string content.
	w = toWire(NewOpenAICompat("https://api.deepseek.com/v1", "k"), req)
	for i, m := range w.Messages {
		if got, ok := m.Content.(string); !ok {
			t.Fatalf("text-only adapter: message %d content should stay a string, got %#v", i, m.Content)
		} else if i == 1 && got != "what is this" {
			t.Fatalf("message 1 should keep its text, got %q", got)
		} else if i == 2 && got != "" {
			t.Fatalf("image-only message should degrade to empty text, got %q", got)
		}
	}
}

func TestSupportsImages(t *testing.T) {
	if NewOpenAICompat("http://localhost:11434/v1", "").SupportsImages() {
		t.Fatal("plain OpenAICompat must be text-only")
	}
	if !NewOpenAICompatVision("http://localhost:11434/v1", "").SupportsImages() {
		t.Fatal("vision constructor must claim image support")
	}
}

// A backend that accepts the request, streams a little, then goes silent
// forever must not hold the turn open. Observed in the field: the
// connection stayed ESTABLISHED with zero bytes for minutes and the loop
// waited with it, so the chat was stuck until the process restarted.
func TestStreamStallTimeout(t *testing.T) {
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("no flusher")
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		fl.Flush()
		<-release // then say nothing at all, forever
	}))
	// Order matters: the handler must be released before Close waits on it.
	defer srv.Close()
	defer close(release)

	p := NewOpenAICompat(srv.URL, "")
	p.StallTimeout = 150 * time.Millisecond

	ch, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	var gotText bool
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			switch ev.Kind {
			case EventTextDelta:
				gotText = true
			case EventError:
				streamErr = ev.Err
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stream never ended: the stall watchdog did not fire")
	}

	if !gotText {
		t.Error("the delta sent before the stall was lost")
	}
	if streamErr == nil {
		t.Fatal("stalled stream ended without an error")
	}
	if !strings.Contains(streamErr.Error(), "stalled") {
		t.Errorf("error = %v, want it to name the stall", streamErr)
	}
}

// A stream that keeps producing must never trip the watchdog, however
// long it runs in total: the timeout bounds silence, not duration.
func TestStreamActivityKeepsStreamAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for range 8 {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
			fl.Flush()
			time.Sleep(40 * time.Millisecond)
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL, "")
	p.StallTimeout = 120 * time.Millisecond // shorter than the total run

	ch, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var deltas int
	for ev := range ch {
		if ev.Kind == EventError {
			t.Fatalf("active stream was killed: %v", ev.Err)
		}
		if ev.Kind == EventTextDelta {
			deltas++
		}
	}
	if deltas != 8 {
		t.Errorf("deltas = %d, want 8", deltas)
	}
}
