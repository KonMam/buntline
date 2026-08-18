package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// OpenAICompat speaks the OpenAI chat-completions protocol with streaming.
// One adapter covers every server that clones the API: Ollama, vLLM,
// llama.cpp, LM Studio, OpenRouter, DeepSeek, and OpenAI itself.
type OpenAICompat struct {
	BaseURL string // e.g. http://localhost:11434/v1
	APIKey  string // optional; local servers ignore it
	Client  *http.Client
	// StallTimeout bounds the silence between events on a live stream
	// (0 = defaultStallTimeout). Not an overall deadline: a legitimate
	// turn runs for minutes, but a backend that has gone quiet must not
	// hold the loop with it.
	StallTimeout time.Duration
	// vision marks backends that accept image_url content parts. Off by
	// default: most OpenAI-compatible APIs are text-only and reject the
	// part outright (DeepSeek's 400 above), so image support is claimed
	// per endpoint, not assumed. Ollama's /v1 adapter is constructed
	// with it on.
	vision bool
}

// defaultStallTimeout is how long a stream may say nothing at all before
// the adapter gives up on it. Generous on purpose: it is a liveness
// check, not a latency budget. Time-to-first-token on a large prompt is
// seconds, and a reasoning model streams its thinking, so two minutes of
// total silence means the connection is dead, not slow.
const defaultStallTimeout = 120 * time.Second

func NewOpenAICompat(baseURL, apiKey string) *OpenAICompat {
	// No overall timeout: streams are long-lived and a legitimate turn
	// can run for minutes. Bound the phases that must be quick instead.
	// Response headers arrive before generation starts (a backend that
	// has not sent them is not thinking, it is gone), and the stall
	// watchdog covers silence after them.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &OpenAICompat{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Client:  &http.Client{Transport: tr},
	}
}

func (p *OpenAICompat) stallTimeout() time.Duration {
	if p.StallTimeout > 0 {
		return p.StallTimeout
	}
	return defaultStallTimeout
}

// stallWatch cancels a stream that has gone silent. The reader pokes it
// on every event; when the gap between pokes exceeds the timeout it
// cancels the request, which unblocks the reader's blocked Read.
//
// Cancellation is the only mechanism that works here: the reader is
// parked inside a Read on the response body, so nothing short of tearing
// the request down can wake it. fired records that the cancel was ours,
// so the reader reports a stall rather than a bare "context canceled"
// that the user cannot tell from their own interrupt.
type stallWatch struct {
	activity chan struct{}
	fired    atomic.Bool
}

func newStallWatch() *stallWatch {
	return &stallWatch{activity: make(chan struct{}, 1)}
}

// poke records stream activity. Non-blocking: the signal is a hint, and
// one pending poke already means "not stalled".
func (w *stallWatch) poke() {
	select {
	case w.activity <- struct{}{}:
	default:
	}
}

func (w *stallWatch) run(ctx context.Context, cancel context.CancelFunc, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.activity:
			t.Reset(d) // Go 1.23+ timers need no drain before Reset
		case <-t.C:
			w.fired.Store(true)
			cancel()
			return
		}
	}
}

// NewOpenAICompatVision is NewOpenAICompat for endpoints that accept
// image content parts (local Ollama). Everything else stays text-only.
func NewOpenAICompatVision(baseURL, apiKey string) *OpenAICompat {
	p := NewOpenAICompat(baseURL, apiKey)
	p.vision = true
	return p
}

func (p *OpenAICompat) Name() string { return "openai-compat" }

func (p *OpenAICompat) SupportsImages() bool { return p.vision }

// ListModels returns the model IDs the endpoint serves, via the one
// capability every OpenAI-compatible server shares: GET /v1/models.
// Ollama, LM Studio, llama.cpp, vLLM, and every hosted API answer it, so
// the Models view lists any provider's models through this single call.
func (p *OpenAICompat) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := p.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode /models: %w", err)
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		if m.ID != "" {
			names = append(names, m.ID)
		}
	}
	sort.Strings(names)
	return names, nil
}

// --- wire format ---

type wireMessage struct {
	Role string `json:"role"`
	// Content is a string for ordinary messages, or an array of content
	// parts ({type:"text"} / {type:"image_url"}) when images are attached.
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type wireRequest struct {
	Model         string        `json:"model"`
	Messages      []wireMessage `json:"messages"`
	Tools         []wireTool    `json:"tools,omitempty"`
	Stream        bool          `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

// chunk is one streamed completion delta. Field names cover the dialect
// spread: reasoning_content (DeepSeek) / reasoning (some servers) for
// thinking tokens, prompt_tokens_details.cached_tokens (OpenAI) and
// prompt_cache_hit_tokens (DeepSeek) for cache visibility.
type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string          `json:"content"`
			Reasoning        string          `json:"reasoning"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []chunkToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type chunkToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func toWire(p *OpenAICompat, req Request) wireRequest {
	w := wireRequest{Model: req.Model, Stream: true}
	w.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}
	for _, m := range req.Messages {
		wm := wireMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		if p.vision && len(m.Images) > 0 {
			parts := make([]wirePart, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, wirePart{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				p := wirePart{Type: "image_url"}
				p.ImageURL = &struct {
					URL string `json:"url"`
				}{URL: img}
				parts = append(parts, p)
			}
			wm.Content = parts
		}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireFunction{
					Name:      tc.Name,
					Arguments: tc.Args,
				},
			})
		}
		w.Messages = append(w.Messages, wm)
	}
	for _, t := range req.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		w.Tools = append(w.Tools, wt)
	}
	return w
}

// Stream implements Provider. It retries the initial connection on
// transient failures; once the stream has started, errors surface as
// EventError (mid-stream resume is not a thing in this protocol).
func (p *OpenAICompat) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	body, err := json.Marshal(toWire(p, req))
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	// The stream gets a context this adapter owns, so the watchdog can
	// cut a silent connection loose without help from the caller. It is
	// cancelled when the reader finishes, whatever ends it.
	ctx, cancel := context.WithCancel(ctx)

	resp, err := p.post(ctx, body)
	if err != nil {
		cancel()
		return nil, err
	}

	out := make(chan Event, 16)
	w := newStallWatch()
	go w.run(ctx, cancel, p.stallTimeout())
	go func() {
		defer cancel()
		p.readStream(ctx, resp.Body, out, w)
	}()
	return out, nil
}

// post sends the request with bounded retries on connection errors,
// 429s, and 5xx responses. 4xx (other than 429) fails immediately.
func (p *OpenAICompat) post(ctx context.Context, body []byte) (*http.Response, error) {
	const maxAttempts = 3
	backoff := 500 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff):
				backoff *= 2
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			p.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if p.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
		}

		resp, err := p.Client.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return nil, lastErr
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

// toolCallAccumulator reassembles tool calls from streamed fragments. The
// protocol keys fragments by index: the first fragment for an index carries
// id and name, later ones append argument JSON.
type toolCallAccumulator map[int]*ToolCall

func (a toolCallAccumulator) add(c chunkToolCall) {
	tc, ok := a[c.Index]
	if !ok {
		tc = &ToolCall{}
		a[c.Index] = tc
	}
	if c.ID != "" {
		tc.ID = c.ID
	}
	if c.Function.Name != "" {
		tc.Name = c.Function.Name
	}
	tc.Args += c.Function.Arguments
}

func (a toolCallAccumulator) calls() []ToolCall {
	idx := make([]int, 0, len(a))
	for i := range a {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]ToolCall, 0, len(a))
	for _, i := range idx {
		out = append(out, *a[i])
	}
	return out
}

func (p *OpenAICompat) readStream(ctx context.Context, body io.ReadCloser, out chan<- Event, w *stallWatch) {
	defer close(out)
	defer func() { _ = body.Close() }()

	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// A terminal error must reach the caller even when the context is
	// already cancelled: a stall cancels ctx itself, so gating this on
	// ctx.Done() would swallow the very event that explains the stop and
	// leave the caller staring at a silently closed channel. Bounded, so
	// a consumer that walked away cannot leak this goroutine.
	sendFinal := func(ev Event) {
		t := time.NewTimer(2 * time.Second)
		defer t.Stop()
		select {
		case out <- ev:
		case <-t.C:
		}
	}

	sc := newSSEScanner(body)
	acc := toolCallAccumulator{}
	var usage *Usage
	finish := "stop"

	for {
		data, err := sc.Next()
		w.poke()
		if err == io.EOF {
			break
		}
		if err != nil {
			switch {
			case w.fired.Load():
				// Our own cancellation. Say so plainly: "context
				// canceled" reads like the user pressed stop.
				err = fmt.Errorf("stream stalled: no data from the model for %s", p.stallTimeout())
			case ctx.Err() != nil:
				err = ctx.Err()
			}
			sendFinal(Event{Kind: EventError, Err: err})
			return
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}

		var c chunk
		if err := json.Unmarshal(data, &c); err != nil {
			sendFinal(Event{Kind: EventError, Err: fmt.Errorf("bad chunk: %w", err)})
			return
		}
		if c.Error != nil {
			sendFinal(Event{Kind: EventError, Err: fmt.Errorf("provider: %s", c.Error.Message)})
			return
		}

		if c.Usage != nil {
			usage = &Usage{
				PromptTokens:     c.Usage.PromptTokens,
				CompletionTokens: c.Usage.CompletionTokens,
			}
			if c.Usage.PromptTokensDetails != nil {
				usage.CachedTokens = c.Usage.PromptTokensDetails.CachedTokens
			}
			if c.Usage.PromptCacheHitTokens > 0 {
				usage.CachedTokens = c.Usage.PromptCacheHitTokens
			}
		}
		if len(c.Choices) == 0 {
			continue // usage-only final chunk
		}

		choice := c.Choices[0]
		if think := choice.Delta.ReasoningContent + choice.Delta.Reasoning; think != "" {
			if !send(Event{Kind: EventThinkingDelta, Text: think}) {
				return
			}
		}
		if choice.Delta.Content != "" {
			if !send(Event{Kind: EventTextDelta, Text: choice.Delta.Content}) {
				return
			}
		}
		for _, tc := range choice.Delta.ToolCalls {
			acc.add(tc)
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			finish = *choice.FinishReason
		}
	}

	if calls := acc.calls(); len(calls) > 0 {
		if !send(Event{Kind: EventToolCalls, ToolCalls: calls}) {
			return
		}
	}
	if usage != nil {
		if !send(Event{Kind: EventUsage, Usage: usage}) {
			return
		}
	}
	send(Event{Kind: EventDone, FinishReason: finish})
}
