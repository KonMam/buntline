// Package search contributes a web_search tool backed by a configured
// engine: a self-hosted SearXNG instance or the Brave Search API. Without
// configuration the module contributes no tool and the store card says so.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KonMam/buntline/internal/config"
	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

type Module struct {
	Cfg config.Search
}

func (m *Module) Info() module.Info {
	desc := "Adds a web_search tool."
	if !m.configured() {
		desc += " Needs configuration: set [search] provider (searxng or brave), url or api_key in buntline.toml."
	} else {
		desc += fmt.Sprintf(" Using %s.", m.Cfg.Provider)
	}
	return module.Info{
		ID:          "search",
		Name:        "Web search",
		Description: desc,
		Default:     true,
	}
}

func (m *Module) configured() bool {
	switch m.Cfg.Provider {
	case "searxng":
		return m.Cfg.URL != ""
	case "brave":
		return m.Cfg.APIKey != ""
	}
	return false
}

func (m *Module) Tools(_ string) []tools.Tool {
	if !m.configured() {
		return nil
	}
	return []tools.Tool{&Tool{Cfg: m.Cfg, Client: &http.Client{Timeout: 15 * time.Second}}}
}

type Tool struct {
	Cfg    config.Search
	Client *http.Client
}

// Safe: search sends only the query string to a user-configured endpoint,
// unlike web_fetch where the model composes the full URL.
func (t *Tool) Safe() bool { return true }

func (t *Tool) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "web_search",
		Description: "Search the web. Returns titles, URLs, and snippets; use web_fetch to read a result.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query.",
				},
			},
			"required": []string{"query"},
		},
	}
}

type result struct {
	Title   string
	URL     string
	Snippet string
}

func (t *Tool) Run(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Query) == "" {
		return tools.Result{}, fmt.Errorf("query is required")
	}

	var (
		results []result
		err     error
	)
	switch t.Cfg.Provider {
	case "searxng":
		results, err = t.searxng(ctx, in.Query)
	case "brave":
		results, err = t.brave(ctx, in.Query)
	default:
		return tools.Result{}, fmt.Errorf("search is not configured")
	}
	if err != nil {
		return tools.Result{}, err
	}
	if len(results) == 0 {
		return tools.Result{Content: "no results"}, nil
	}

	var sb strings.Builder
	for i, r := range results {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&sb, "%d. %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Snippet)
	}
	return tools.Result{Content: strings.TrimSpace(sb.String())}, nil
}

func (t *Tool) searxng(ctx context.Context, query string) ([]result, error) {
	u := strings.TrimRight(t.Cfg.URL, "/") + "/search?format=json&q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("searxng: %s: %s", resp.Status, body)
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	results := make([]result, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return results, nil
}

func (t *Tool) brave(ctx context.Context, query string) ([]result, error) {
	u := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", t.Cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := t.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("brave: %s: %s", resp.Status, body)
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	results := make([]result, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		results = append(results, result{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return results, nil
}
