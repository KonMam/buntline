// Package mcpclient connects buntline to external MCP servers: the
// third-party plugin mechanism, deliberately not a bespoke protocol.
// Servers come from buntline.toml ([[mcp_servers]]) or are added in the
// app (persisted to mcp.json); their tools join the registry namespaced
// as <server>_<tool>, side-effectful by default so every call passes the
// approval gate. When the combined tool count is large, the model gets
// two meta-tools (list/call) instead of every schema, keeping the
// request prefix small.
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KonMam/buntline/internal/config"
	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/secrets"
	"github.com/KonMam/buntline/internal/tools"
)

// deferThreshold is the combined MCP tool count above which individual
// schemas stop being sent to the model. Every tool definition rides in
// every request; a few servers can add thousands of prompt tokens, and
// small models measurably stop calling tools as the prefix grows.
const deferThreshold = 12

type Module struct {
	mu         sync.Mutex
	fromConfig []config.MCPServer
	dynamic    []config.MCPServer // app-added, persisted to mcp.json
	sessions   map[string]*mcp.ClientSession
	statuses   map[string]string       // server name → "connected, N tools" / error text
	byServer   map[string][]tools.Tool // server name → its wrapped tools
	// The other two thirds of MCP: prompts (exposed as slash commands)
	// and resources (exposed through read tools).
	prompts   map[string][]*mcp.Prompt
	resources map[string][]*mcp.Resource
	loaded    bool
}

func New(servers []config.MCPServer) *Module {
	return &Module{
		fromConfig: servers,
		dynamic:    config.LoadMCPServers(),
		sessions:   map[string]*mcp.ClientSession{},
		statuses:   map[string]string{},
		byServer:   map[string][]tools.Tool{},
		prompts:    map[string][]*mcp.Prompt{},
		resources:  map[string][]*mcp.Resource{},
	}
}

func (m *Module) allServers() []config.MCPServer {
	return append(append([]config.MCPServer{}, m.fromConfig...), m.dynamic...)
}

func (m *Module) Info() module.Info {
	m.mu.Lock()
	n := len(m.fromConfig) + len(m.dynamic)
	m.mu.Unlock()
	desc := "Connect external MCP servers; their tools join the agent behind the approval gate."
	if n == 0 {
		desc += " Add servers on this page or as [[mcp_servers]] entries in buntline.toml."
	} else {
		desc += fmt.Sprintf(" %d server(s) configured.", n)
	}
	return module.Info{
		ID:          "mcp",
		Name:        "MCP servers",
		Description: desc,
		Default:     true,
	}
}

// Tools connects (once) to every configured server and wraps their tools.
// Connection happens lazily on first session attach so a dead server slows
// down one attach, not startup.
func (m *Module) Tools(_ string) []tools.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connectAllLocked()

	var all []tools.Tool
	for _, srv := range m.allServers() {
		all = append(all, m.byServer[srv.Name]...)
	}
	// Resources ride along regardless of the tool count: two small,
	// safe tools cover every server's resources.
	var extra []tools.Tool
	for _, rs := range m.resources {
		if len(rs) > 0 {
			extra = []tools.Tool{&listResourcesTool{mod: m}, &readResourceTool{mod: m}}
			break
		}
	}
	if len(all) <= deferThreshold {
		return append(all, extra...)
	}
	return append([]tools.Tool{
		&listTool{mod: m},
		&callTool{mod: m},
	}, extra...)
}

// Stop closes every server connection (stdio child processes die with
// their transport) and clears caches. Called when the module is
// disabled; the next enabled use reconnects lazily.
func (m *Module) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, s := range m.sessions {
		_ = s.Close()
		delete(m.sessions, name)
	}
	m.byServer = map[string][]tools.Tool{}
	m.prompts = map[string][]*mcp.Prompt{}
	m.resources = map[string][]*mcp.Resource{}
	m.statuses = map[string]string{}
	m.loaded = false
}

// connectAllLocked dials every not-yet-connected server concurrently:
// attach latency is the slowest server, not the sum. m.mu is held by the
// caller; the goroutines write results through a local mutex.
func (m *Module) connectAllLocked() {
	if m.loaded {
		return
	}
	m.loaded = true
	var wg sync.WaitGroup
	var results sync.Mutex
	for _, srv := range m.allServers() {
		if _, ok := m.byServer[srv.Name]; ok {
			continue
		}
		wg.Add(1)
		go func(srv config.MCPServer) {
			defer wg.Done()
			c, err := m.connect(srv)
			results.Lock()
			defer results.Unlock()
			m.recordConnection(srv.Name, c, err)
		}(srv)
	}
	wg.Wait()
}

func (m *Module) connectOneLocked(srv config.MCPServer) {
	c, err := m.connect(srv)
	m.recordConnection(srv.Name, c, err)
}

// connection is everything one server contributes.
type connection struct {
	session   *mcp.ClientSession
	tools     []tools.Tool
	prompts   []*mcp.Prompt
	resources []*mcp.Resource
}

func (m *Module) recordConnection(name string, c *connection, err error) {
	if err != nil {
		m.statuses[name] = err.Error()
		return
	}
	m.sessions[name] = c.session
	m.byServer[name] = c.tools
	m.prompts[name] = c.prompts
	m.resources[name] = c.resources
	m.statuses[name] = fmt.Sprintf("connected, %d tools", len(c.tools))
}

func (m *Module) connect(srv config.MCPServer) (*connection, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var transport mcp.Transport
	switch srv.Transport {
	case "stdio", "":
		if srv.Command == "" {
			return nil, fmt.Errorf("stdio transport needs a command")
		}
		cmd := exec.Command(srv.Command, srv.Args...)
		if len(srv.Env) > 0 {
			cmd.Env = os.Environ()
			for k, v := range srv.Env {
				cmd.Env = append(cmd.Env, k+"="+resolveEnvValue(v))
			}
		}
		transport = &mcp.CommandTransport{Command: cmd}
	case "http":
		if srv.URL == "" {
			return nil, fmt.Errorf("http transport needs a url")
		}
		transport = &mcp.StreamableClientTransport{Endpoint: srv.URL}
	default:
		return nil, fmt.Errorf("unknown transport %q (stdio or http)", srv.Transport)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "buntline", Version: "0.1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	list, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("list tools: %w", err)
	}

	c := &connection{session: session}
	// Prompts and resources are optional server capabilities; absence is
	// not an error.
	if pr, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{}); err == nil {
		c.prompts = pr.Prompts
	}
	if rr, err := session.ListResources(ctx, &mcp.ListResourcesParams{}); err == nil {
		c.resources = rr.Resources
	}

	for _, t := range list.Tools {
		schema := map[string]any{"type": "object"}
		if t.InputSchema != nil {
			if raw, err := json.Marshal(t.InputSchema); err == nil {
				_ = json.Unmarshal(raw, &schema)
			}
		}
		c.tools = append(c.tools, &remoteTool{
			session: session,
			name:    t.Name,
			def: provider.ToolDef{
				Name:        sanitize(srv.Name + "_" + t.Name),
				Description: fmt.Sprintf("[%s] %s", srv.Name, t.Description),
				Parameters:  schema,
			},
		})
	}
	return c, nil
}

// findLocked resolves a namespaced tool name to its wrapped tool.
func (m *Module) findLocked(name string) tools.Tool {
	for _, ts := range m.byServer {
		for _, t := range ts {
			if t.Def().Name == name {
				return t
			}
		}
	}
	return nil
}

type serverInfo struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	URL       string   `json:"url,omitempty"`
	EnvKeys   []string `json:"env_keys,omitempty"` // keys only; values may be secrets
	Source    string   `json:"source"`             // "config" (read-only) or "app"
	Status    string   `json:"status"`
	Tools     []string `json:"tools"`
}

func (m *Module) listLocked() []serverInfo {
	m.connectAllLocked()
	out := make([]serverInfo, 0, len(m.fromConfig)+len(m.dynamic))
	add := func(srv config.MCPServer, source string) {
		info := serverInfo{
			Name:      srv.Name,
			Transport: srv.Transport,
			Command:   srv.Command,
			Args:      srv.Args,
			URL:       srv.URL,
			Source:    source,
			Status:    m.statuses[srv.Name],
			Tools:     []string{},
		}
		if info.Transport == "" {
			info.Transport = "stdio"
		}
		for k := range srv.Env {
			info.EnvKeys = append(info.EnvKeys, k)
		}
		sort.Strings(info.EnvKeys)
		for _, t := range m.byServer[srv.Name] {
			info.Tools = append(info.Tools, t.Def().Name)
		}
		sort.Strings(info.Tools)
		out = append(out, info)
	}
	for _, srv := range m.fromConfig {
		add(srv, "config")
	}
	for _, srv := range m.dynamic {
		add(srv, "app")
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// Routes: server management. Changes take effect for sessions attached
// after the change; already-attached sessions keep their tool set.
func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /servers": func(w http.ResponseWriter, _ *http.Request) {
			m.mu.Lock()
			servers := m.listLocked()
			m.mu.Unlock()
			writeJSON(w, map[string]any{"servers": servers})
		},

		"POST /servers": func(w http.ResponseWriter, r *http.Request) {
			var srv config.MCPServer
			if err := json.NewDecoder(r.Body).Decode(&srv); err != nil {
				httpErr(w, http.StatusBadRequest, err)
				return
			}
			srv.Name = strings.TrimSpace(srv.Name)
			if srv.Name == "" {
				httpErr(w, http.StatusBadRequest, fmt.Errorf("name is required"))
				return
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			for _, existing := range m.allServers() {
				if existing.Name == srv.Name {
					httpErr(w, http.StatusConflict, fmt.Errorf("a server named %q already exists", srv.Name))
					return
				}
			}
			m.dynamic = append(m.dynamic, srv)
			if err := config.SaveMCPServers(m.dynamic); err != nil {
				m.dynamic = m.dynamic[:len(m.dynamic)-1]
				httpErr(w, http.StatusInternalServerError, err)
				return
			}
			m.connectOneLocked(srv)
			writeJSON(w, map[string]any{"servers": m.listLocked()})
		},

		"DELETE /servers/{name}": func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			m.mu.Lock()
			defer m.mu.Unlock()
			idx := -1
			for i, srv := range m.dynamic {
				if srv.Name == name {
					idx = i
					break
				}
			}
			if idx < 0 {
				httpErr(w, http.StatusNotFound, fmt.Errorf("no app-managed server named %q (config.toml entries are edited in the file)", name))
				return
			}
			m.dynamic = append(m.dynamic[:idx], m.dynamic[idx+1:]...)
			if err := config.SaveMCPServers(m.dynamic); err != nil {
				httpErr(w, http.StatusInternalServerError, err)
				return
			}
			if s, ok := m.sessions[name]; ok {
				_ = s.Close()
				delete(m.sessions, name)
			}
			delete(m.byServer, name)
			delete(m.prompts, name)
			delete(m.resources, name)
			delete(m.statuses, name)
			writeJSON(w, map[string]any{"servers": m.listLocked()})
		},

		"POST /servers/{name}/reconnect": func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			m.mu.Lock()
			defer m.mu.Unlock()
			var target *config.MCPServer
			for _, srv := range m.allServers() {
				if srv.Name == name {
					target = &srv
					break
				}
			}
			if target == nil {
				httpErr(w, http.StatusNotFound, fmt.Errorf("no server named %q", name))
				return
			}
			if s, ok := m.sessions[name]; ok {
				_ = s.Close()
				delete(m.sessions, name)
			}
			delete(m.byServer, name)
			delete(m.prompts, name)
			delete(m.resources, name)
			m.connectOneLocked(*target)
			writeJSON(w, map[string]any{"servers": m.listLocked()})
		},

		// MCP prompts surface as slash commands in the composer: list
		// them, and render one to the text the user sends.
		"GET /prompts": func(w http.ResponseWriter, _ *http.Request) {
			m.mu.Lock()
			m.connectAllLocked()
			type promptInfo struct {
				Name        string   `json:"name"` // "<server>/<prompt>"
				Description string   `json:"description"`
				Arguments   []string `json:"arguments"`
			}
			out := []promptInfo{}
			for _, srv := range m.allServers() {
				for _, p := range m.prompts[srv.Name] {
					info := promptInfo{
						Name:        srv.Name + "/" + p.Name,
						Description: p.Description,
						Arguments:   []string{},
					}
					for _, a := range p.Arguments {
						info.Arguments = append(info.Arguments, a.Name)
					}
					out = append(out, info)
				}
			}
			m.mu.Unlock()
			writeJSON(w, map[string]any{"prompts": out})
		},

		"POST /prompts/render": func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Name     string `json:"name"`     // "<server>/<prompt>"
				Argument string `json:"argument"` // free text, mapped to the first declared argument
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				httpErr(w, http.StatusBadRequest, err)
				return
			}
			serverName, promptName, ok := strings.Cut(in.Name, "/")
			if !ok {
				httpErr(w, http.StatusBadRequest, fmt.Errorf("name must be server/prompt"))
				return
			}
			// Resolve the session and prompt under the lock, then release
			// it before the network round-trip below. Holding m.mu across
			// GetPrompt would deadlock: the server-side handler for a
			// prompt can call back into this module's tools, and those
			// take the same lock (the mcpclient tests' servers do exactly
			// that).
			m.mu.Lock()
			session := m.sessions[serverName]
			var prompt *mcp.Prompt
			for _, p := range m.prompts[serverName] {
				if p.Name == promptName {
					prompt = p
					break
				}
			}
			m.mu.Unlock()
			if session == nil || prompt == nil {
				httpErr(w, http.StatusNotFound, fmt.Errorf("no prompt %q", in.Name))
				return
			}
			args := map[string]string{}
			if in.Argument != "" && len(prompt.Arguments) > 0 {
				args[prompt.Arguments[0].Name] = in.Argument
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			res, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: promptName, Arguments: args})
			if err != nil {
				httpErr(w, http.StatusBadGateway, err)
				return
			}
			var sb strings.Builder
			for _, msg := range res.Messages {
				if tc, ok := msg.Content.(*mcp.TextContent); ok {
					sb.WriteString(tc.Text)
					sb.WriteString("\n")
				}
			}
			writeJSON(w, map[string]string{"text": strings.TrimSpace(sb.String())})
		},
	}
}

// listResourcesTool reveals what the connected servers publish as
// resources; readResourceTool fetches one. Both read-only.
type listResourcesTool struct{ mod *Module }

func (t *listResourcesTool) Safe() bool { return true }

func (t *listResourcesTool) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "mcp_list_resources",
		Description: "List the resources published by connected MCP servers: URIs, names, and descriptions. Read one with mcp_read_resource.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (t *listResourcesTool) Run(_ context.Context, _ json.RawMessage) (tools.Result, error) {
	t.mod.mu.Lock()
	defer t.mod.mu.Unlock()
	var sb strings.Builder
	for _, srv := range t.mod.allServers() {
		for _, r := range t.mod.resources[srv.Name] {
			fmt.Fprintf(&sb, "%s: %s", r.URI, r.Name)
			if r.Description != "" {
				fmt.Fprintf(&sb, " (%s)", r.Description)
			}
			sb.WriteString("\n")
		}
	}
	if sb.Len() == 0 {
		return tools.Result{Content: "no resources published"}, nil
	}
	return tools.Result{Content: strings.TrimSpace(sb.String())}, nil
}

type readResourceTool struct{ mod *Module }

func (t *readResourceTool) Safe() bool { return true }

func (t *readResourceTool) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "mcp_read_resource",
		Description: "Read one MCP resource by URI, as listed by mcp_list_resources.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uri": map[string]any{
					"type":        "string",
					"description": "The resource URI.",
				},
			},
			"required": []string{"uri"},
		},
	}
}

func (t *readResourceTool) Run(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	// Find the server that published this URI.
	t.mod.mu.Lock()
	var session *mcp.ClientSession
	for _, srv := range t.mod.allServers() {
		for _, r := range t.mod.resources[srv.Name] {
			if r.URI == in.URI {
				session = t.mod.sessions[srv.Name]
			}
		}
	}
	t.mod.mu.Unlock()
	if session == nil {
		return tools.Result{Content: fmt.Sprintf("no published resource %q; call mcp_list_resources for the available URIs", in.URI)}, nil
	}
	res, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: in.URI})
	if err != nil {
		return tools.Result{}, err
	}
	var sb strings.Builder
	for _, c := range res.Contents {
		if c.Text != "" {
			sb.WriteString(c.Text)
			sb.WriteString("\n")
		} else if len(c.Blob) > 0 {
			fmt.Fprintf(&sb, "[binary content, %d bytes, %s]\n", len(c.Blob), c.MIMEType)
		}
	}
	content := strings.TrimSpace(sb.String())
	if content == "" {
		content = "(empty resource)"
	}
	if len(content) > 48*1024 {
		content = content[:48*1024] + "\n... [truncated]"
	}
	return tools.Result{Content: content}, nil
}

// listTool is the safe half of the deferred-schema pair: it reveals what
// the connected servers offer, on demand, instead of shipping every
// schema in every request.
type listTool struct{ mod *Module }

func (t *listTool) Safe() bool { return true }

func (t *listTool) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "mcp_list_tools",
		Description: "List the tools available on connected MCP servers: names, descriptions, and parameter schemas. Call this before mcp_call_tool.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Optional substring to filter tool names and descriptions.",
				},
			},
		},
	}
}

func (t *listTool) Run(_ context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(args, &in)
	q := strings.ToLower(in.Query)

	t.mod.mu.Lock()
	defer t.mod.mu.Unlock()
	var sb strings.Builder
	n := 0
	for _, srv := range t.mod.allServers() {
		for _, tool := range t.mod.byServer[srv.Name] {
			def := tool.Def()
			if q != "" && !strings.Contains(strings.ToLower(def.Name), q) &&
				!strings.Contains(strings.ToLower(def.Description), q) {
				continue
			}
			schema, _ := json.Marshal(def.Parameters)
			fmt.Fprintf(&sb, "%s: %s\n  parameters: %s\n", def.Name, def.Description, schema)
			n++
		}
	}
	if n == 0 {
		return tools.Result{Content: "no matching tools"}, nil
	}
	return tools.Result{Content: strings.TrimSpace(sb.String())}, nil
}

// callTool dispatches to any MCP tool by name. Side-effectful: the
// approval gate shows the real target name and arguments.
type callTool struct{ mod *Module }

func (t *callTool) Safe() bool { return false }

func (t *callTool) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "mcp_call_tool",
		Description: "Call a tool on a connected MCP server by its full name (as returned by mcp_list_tools), with the arguments its schema requires.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Full tool name from mcp_list_tools.",
				},
				"arguments": map[string]any{
					"type":        "object",
					"description": "Arguments matching the tool's parameter schema.",
				},
			},
			"required": []string{"name"},
		},
	}
}

func (t *callTool) Run(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	t.mod.mu.Lock()
	target := t.mod.findLocked(in.Name)
	t.mod.mu.Unlock()
	if target == nil {
		return tools.Result{Content: fmt.Sprintf("unknown tool %q; call mcp_list_tools for the available names", in.Name)}, nil
	}
	return target.Run(ctx, in.Arguments)
}

// remoteTool adapts one MCP tool to buntline's Tool interface.
type remoteTool struct {
	session *mcp.ClientSession
	name    string // original name on the server
	def     provider.ToolDef
}

// External tools are side-effectful until proven otherwise: every call
// goes through the approval gate.
func (t *remoteTool) Safe() bool { return false }

func (t *remoteTool) Def() provider.ToolDef { return t.def }

func (t *remoteTool) Run(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	var arguments map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return tools.Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	res, err := t.session.CallTool(ctx, &mcp.CallToolParams{Name: t.name, Arguments: arguments})
	if err != nil {
		return tools.Result{}, err
	}

	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
			sb.WriteString("\n")
		}
	}
	content := strings.TrimSpace(sb.String())
	if content == "" {
		content = "(no text content)"
	}
	if len(content) > 48*1024 {
		content = content[:48*1024] + "\n... [truncated]"
	}
	if res.IsError {
		return tools.Result{Content: "error: " + content}, nil
	}
	return tools.Result{Content: content}, nil
}

// resolveEnvValue expands ${secret:NAME} from buntline's secrets store and
// ${VAR} from the process environment. Resolution happens at connect
// time so stored config never contains the secret itself.
func resolveEnvValue(v string) string {
	v = secretRe.ReplaceAllStringFunc(v, func(m string) string {
		name := secretRe.FindStringSubmatch(m)[1]
		return secrets.Get(name)
	})
	return os.ExpandEnv(v)
}

var secretRe = regexp.MustCompile(`\$\{secret:([^}]+)\}`)

// sanitize makes a server_tool name safe for model tool-calling.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}
