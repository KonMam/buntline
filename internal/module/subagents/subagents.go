// Package subagents is the store-facing toggle for the spawn_agent tool.
// The tool itself is wired by the server (it needs the session's provider
// and event stream); this module only carries identity and the on/off
// switch.
package subagents

import "github.com/KonMam/tether/internal/module"

type Module struct{}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "subagents",
		Name:        "Subagents",
		Description: "Adds a spawn_agent tool: an isolated child loop investigates with its own context and reports back, keeping the main context lean.",
		Default:     true,
	}
}
