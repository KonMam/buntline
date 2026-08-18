// Package vision adds image-to-text translation: when a session's main
// provider cannot see images (DeepSeek and most hosted APIs), a
// configured vision model describes the attached images and the
// description rides in the message text the main model receives. The
// backend is any OpenAI-compatible endpoint that accepts image_url
// content parts; local Ollama and hosted vision APIs work through the
// same code path. Without configuration the module does nothing, and
// image sends on text-only providers refuse exactly as before.
package vision

import (
	"context"
	"fmt"
	"strings"

	"github.com/KonMam/tether/internal/config"
	"github.com/KonMam/tether/internal/module"
	"github.com/KonMam/tether/internal/provider"
)

// Module is the vision-translation feature. The server's send gate
// consults it when a session's provider is text-only and the user
// attached images.
type Module struct {
	Cfg config.Vision
	// Provider builds the vision client; tests stub it. Defaults to a
	// real OpenAI-compatible client on the configured endpoint.
	Provider func(baseURL, apiKey string) provider.Provider
}

func (m *Module) Info() module.Info {
	desc := "Describes attached images with a vision model when the main model cannot see them."
	if !m.Configured() {
		desc += " Needs configuration: set [vision] base_url and model in tether.toml."
	} else {
		desc += fmt.Sprintf(" Using %s.", m.Cfg.Model)
	}
	return module.Info{
		ID:          "vision",
		Name:        "Vision",
		Description: desc,
		Default:     true,
	}
}

// Configured reports whether a usable vision backend is set up.
func (m *Module) Configured() bool { return m.Cfg.Configured() }

// Model returns the configured vision model name, used in trace labels.
func (m *Module) Model() string { return m.Cfg.Model }

func (m *Module) client() provider.Provider {
	if m.Provider != nil {
		return m.Provider(m.Cfg.BaseURL, m.Cfg.APIKey)
	}
	return provider.NewOpenAICompatVision(m.Cfg.BaseURL, m.Cfg.APIKey)
}

// Describe sends the images to the vision model and returns its text
// description, numbered in the order the images were attached. The main
// model is text-only, so this text is its only view of the images; the
// prompt is tuned to be dense, objective, and complete.
func (m *Module) Describe(ctx context.Context, images []string) (string, error) {
	if !m.Configured() {
		return "", fmt.Errorf("vision is not configured: set [vision] base_url and model in tether.toml")
	}
	if len(images) == 0 {
		return "", fmt.Errorf("no images to describe")
	}
	req := provider.Request{
		Model: m.Cfg.Model,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: captionPrompt,
			Images:  images,
		}},
	}
	events, err := m.client().Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("vision model %s: %w", m.Cfg.Model, err)
	}
	var sb strings.Builder
	for ev := range events {
		switch ev.Kind {
		case provider.EventTextDelta:
			sb.WriteString(ev.Text)
		case provider.EventError:
			return "", fmt.Errorf("vision model %s: %w", m.Cfg.Model, ev.Err)
		}
	}
	desc := strings.TrimSpace(sb.String())
	if desc == "" {
		return "", fmt.Errorf("vision model %s returned an empty description", m.Cfg.Model)
	}
	return desc, nil
}

// captionPrompt is the quality lever of the whole feature: the
// description is the text-only model's only view of the image, so it
// must carry everything a developer would act on (verbatim visible
// text, layout, and state) with no editorializing that loses detail.
const captionPrompt = `You are an image-to-text translator for a coding agent whose main model cannot see images. The user attached one or more images to a message. Describe each image thoroughly and objectively, numbered in the order given (Image 1, Image 2, ...).

Include everything a developer would need to act on the image: visible text verbatim where it matters (error messages, terminal output, code, UI labels, dialogs), the layout and relative position of elements, and the state of any UI or diagram. Be specific and complete; do not interpret or advise. If an image cannot be interpreted, say so plainly.`
