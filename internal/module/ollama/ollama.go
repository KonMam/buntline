// Package ollama is the model-management module: when the provider is an
// Ollama server, it can do what a generic OpenAI-compatible endpoint
// can't: list installed models, show what's loaded, pull new models with
// progress, and judge whether a model fits this machine's memory.
package ollama

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KonMam/buntline/internal/module"
)

type Module struct {
	// BaseURL is the OpenAI-compat URL from config (e.g. .../v1); the
	// native Ollama API lives at its origin.
	BaseURL string
	Client  *http.Client

	ramOnce  sync.Once
	totalRAM int64
}

func New(baseURL string) *Module {
	// This module manages the local Ollama server specifically, so its
	// standard endpoint is this module's own default; config no longer
	// carries a baked-in provider URL to inherit.
	if baseURL == "" {
		baseURL = "http://localhost:11434/v1"
	}
	return &Module{BaseURL: baseURL, Client: &http.Client{Timeout: 30 * time.Second}}
}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "ollama",
		Name:        "Ollama models",
		Description: "List, download, and manage local Ollama models, with memory-fit hints for this machine.",
		Default:     true,
	}
}

func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /models":    m.handleModels,
		"GET /ps":        m.handlePS,
		"POST /pull":     m.handlePull,
		"DELETE /models": m.handleDelete,
		"GET /context":   m.handleContext,
	}
}

// origin strips the /v1 path: http://localhost:11434/v1 → http://localhost:11434
func (m *Module) origin() (string, error) {
	u, err := url.Parse(m.BaseURL)
	if err != nil {
		return "", err
	}
	u.Path, u.RawQuery = "", ""
	return u.String(), nil
}

// TotalRAM reports installed memory. On Apple Silicon this is unified
// memory, which is what actually bounds local model size.
func (m *Module) TotalRAM() int64 {
	m.ramOnce.Do(func() {
		switch runtime.GOOS {
		case "darwin":
			out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
			if err == nil {
				m.totalRAM, _ = strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
			}
		case "linux":
			out, err := exec.Command("grep", "MemTotal", "/proc/meminfo").Output()
			if err == nil {
				fields := strings.Fields(string(out))
				if len(fields) >= 2 {
					kb, _ := strconv.ParseInt(fields[1], 10, 64)
					m.totalRAM = kb * 1024
				}
			}
		}
	})
	return m.totalRAM
}

// fit classifies model size against memory: the weights need to fit with
// room left for the KV cache and the rest of the system. Thresholds are
// heuristic and documented as such in the UI.
func (m *Module) fit(sizeBytes int64) string {
	ram := m.TotalRAM()
	if ram == 0 {
		return "unknown"
	}
	switch ratio := float64(sizeBytes) / float64(ram); {
	case ratio < 0.5:
		return "comfortable"
	case ratio < 0.75:
		return "tight"
	default:
		return "too_large"
	}
}

func (m *Module) handleModels(w http.ResponseWriter, r *http.Request) {
	origin, err := m.origin()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	resp, err := m.Client.Get(origin + "/api/tags")
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("ollama unreachable at %s: %w", origin, err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}

	type modelInfo struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Params string `json:"params"`
		Quant  string `json:"quant"`
		Fit    string `json:"fit"`
	}
	models := []modelInfo{}
	for _, mm := range tags.Models {
		models = append(models, modelInfo{
			Name:   mm.Name,
			Size:   mm.Size,
			Params: mm.Details.ParameterSize,
			Quant:  mm.Details.QuantizationLevel,
			Fit:    m.fit(mm.Size),
		})
	}
	writeJSON(w, map[string]any{"models": models, "total_ram": m.TotalRAM()})
}

func (m *Module) handlePS(w http.ResponseWriter, r *http.Request) {
	origin, err := m.origin()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	resp, err := m.Client.Get(origin + "/api/ps")
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, resp.Body)
}

// handlePull proxies Ollama's streaming pull as SSE so the store UI can
// render a progress bar. Downloads can take many minutes; no client
// timeout applies here.
func (m *Module) handlePull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("model name is required"))
		return
	}
	origin, err := m.origin()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}

	body, _ := json.Marshal(map[string]any{"model": in.Name, "stream": true})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		origin+"/api/pull", strings.NewReader(string(body)))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	resp, err := (&http.Client{}).Do(req) // no timeout: long download
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	dec := json.NewDecoder(resp.Body)
	for {
		var progress json.RawMessage
		if err := dec.Decode(&progress); err != nil {
			return // stream ended (or client left)
		}
		fmt.Fprintf(w, "data: %s\n\n", progress)
		flusher.Flush()
	}
}

// handleContext reports a model's context window, from the
// "<arch>.context_length" key in Ollama's show API.
func (m *Module) handleContext(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	if model == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("model is required"))
		return
	}
	origin, err := m.origin()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	body, _ := json.Marshal(map[string]string{"model": model})
	resp, err := m.Client.Post(origin+"/api/show", "application/json", strings.NewReader(string(body)))
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var show struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&show); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	var contextLength int64
	for k, v := range show.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if f, ok := v.(float64); ok {
				contextLength = int64(f)
			}
		}
	}
	writeJSON(w, map[string]any{"context_length": contextLength})
}

// handleDelete removes a local model via Ollama's delete API.
func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("model name is required"))
		return
	}
	origin, err := m.origin()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	body, _ := json.Marshal(map[string]string{"model": in.Name})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodDelete,
		origin+"/api/delete", strings.NewReader(string(body)))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		fail(w, resp.StatusCode, fmt.Errorf("ollama: %s", strings.TrimSpace(string(msg))))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Stop unloads every model currently loaded into Ollama memory, the
// promise behind "disabled means zero cost": a model the agent loaded
// (and any other the server has resident) releases its RAM when the
// module is switched off. It is the registry's Stop hook; a re-enable
// brings the management routes back and the next run loads fresh.
func (m *Module) Stop() {
	origin, err := m.origin()
	if err != nil {
		return // no origin, nothing to unload
	}
	// Loaded models first, so unload knows who to evict.
	var ps struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if resp, err := m.Client.Get(origin + "/api/ps"); err == nil {
		_ = json.NewDecoder(resp.Body).Decode(&ps)
		_ = resp.Body.Close()
	}
	for _, model := range ps.Models {
		if strings.TrimSpace(model.Name) == "" {
			continue
		}
		body, _ := json.Marshal(map[string]any{"model": model.Name, "keep_alive": 0})
		if resp, err := m.Client.Post(origin+"/api/generate", "application/json",
			strings.NewReader(string(body))); err == nil {
			_ = resp.Body.Close()
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
