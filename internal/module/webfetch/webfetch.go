// Package webfetch contributes a web_fetch tool: fetch a URL and return
// its text. Approval-gated even though it's a read: a fetched page is
// untrusted input for the model, and a URL built by the model can leak
// context to whoever runs the server; the user should see it first.
package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

type Module struct{}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "webfetch",
		Name:        "Web fetch",
		Description: "Adds a web_fetch tool: the agent can read a page's text, with your approval per call.",
		Default:     true,
	}
}

func (m *Module) Tools(_ string) []tools.Tool {
	return []tools.Tool{&Tool{Client: &http.Client{Timeout: 20 * time.Second}}}
}

type Tool struct {
	Client *http.Client
}

func (t *Tool) Safe() bool { return false }

func (t *Tool) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "web_fetch",
		Description: "Fetch a URL over HTTP(S) and return its visible text content.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "Absolute http(s) URL to fetch.",
				},
			},
			"required": []string{"url"},
		},
	}
}

const fetchCap = 2 << 20 // response body read limit

func (t *Tool) Run(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		return tools.Result{}, fmt.Errorf("url must be absolute http(s), got %q", in.URL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return tools.Result{}, err
	}
	req.Header.Set("User-Agent", "buntline/1.0 (+local harness)")
	resp, err := t.Client.Do(req)
	if err != nil {
		return tools.Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchCap))
	if err != nil {
		return tools.Result{}, err
	}

	text := string(body)
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		text = stripHTML(text)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return tools.Result{Content: fmt.Sprintf("(%s: empty or non-text content)", resp.Status)}, nil
	}
	if len(text) > 48*1024 {
		text = text[:48*1024] + "\n... [truncated]"
	}
	return tools.Result{Content: text}, nil
}

var (
	dropRe  = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head)[^>]*>.*?</\s*(script|style|noscript|svg|head)\s*>`)
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	blankRe = regexp.MustCompile(`\n{3,}`)
)

// stripHTML is a deliberately naive tag stripper: good enough to hand a
// model readable text, not a rendering engine. Block-ish tags become
// newlines so structure survives.
func stripHTML(s string) string {
	s = dropRe.ReplaceAllString(s, " ")
	s = regexp.MustCompile(`(?i)</(p|div|li|tr|h[1-6]|br|section|article)>|<br\s*/?>`).ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ").Replace(s)
	// Collapse intra-line whitespace, keep paragraph breaks.
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	s = strings.Join(lines, "\n")
	return blankRe.ReplaceAllString(strings.TrimSpace(s), "\n\n")
}
