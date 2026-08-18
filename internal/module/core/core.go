// Package core hosts the modules that are part of the harness itself
// rather than toggleable features. Their tool contributions are decided
// at build time, not by the runtime module toggle, so the agent always
// has the surface it needs to work a repository. The store lists them in
// a read-only section; the toggleable features live in internal/module.
//
// Core IDs are distinct from the feature module IDs: "core_files" is the
// agent's file tools, while the toggleable "files" module is the browser
// UI. One is the working surface, the other a view. Registration order
// in main.go is the order the tools reach the model, matching the
// built-in registry's historical order.
package core

import (
	"github.com/KonMam/tether/internal/module"
	"github.com/KonMam/tether/internal/tools"
)

// Files is the agent's file surface: read, write, edit. The harness
// cannot work a repository without it, so it is core.
type Files struct{}

func (m *Files) Info() module.Info {
	return module.Info{
		ID:          "core_files",
		Name:        "File tools",
		Description: "The agent's file surface: read, write, and edit files in the working directory.",
		Core:        true,
	}
}

func (m *Files) Tools(workdir string) []tools.Tool {
	return tools.FileTools(workdir)
}

// Bash is the agent's shell: run commands, background tasks, and follow
// their output. The harness acts through it, so it is core.
type Bash struct{}

func (m *Bash) Info() module.Info {
	return module.Info{
		ID:          "core_bash",
		Name:        "Shell",
		Description: "The agent's shell: run commands, background tasks, and follow their output.",
		Core:        true,
	}
}

func (m *Bash) Tools(workdir string) []tools.Tool {
	return tools.BashTools(workdir)
}

// Search is the agent's repository search: grep and glob. Reading a
// codebase is how it orients itself, so it is core.
type Search struct{}

func (m *Search) Info() module.Info {
	return module.Info{
		ID:          "core_search",
		Name:        "Repo search",
		Description: "The agent's repository search: grep for patterns and glob for files.",
		Core:        true,
	}
}

func (m *Search) Tools(workdir string) []tools.Tool {
	return tools.GrepTools(workdir)
}

// Ask is the ask_user tool: the model pauses a turn to ask the user a
// real question instead of guessing. A harness without it is one-way,
// so it is core. The server wires the answerer to the browser.
type Ask struct{}

func (m *Ask) Info() module.Info {
	return module.Info{
		ID:          "core_ask_user",
		Name:        "Ask user",
		Description: "The model can pause a turn and ask you a question instead of guessing.",
		Core:        true,
	}
}

func (m *Ask) Tools(workdir string) []tools.Tool {
	return []tools.Tool{tools.AskUserTool()}
}

// SessionSearch is the session_search tool: the agent recalls what past
// sessions did. The server wires the searcher to the session store,
// mirroring the ask_user answerer pattern.
type SessionSearch struct{}

func (m *SessionSearch) Info() module.Info {
	return module.Info{
		ID:          "core_session_search",
		Name:        "Session search",
		Description: "The agent can recall what past sessions did.",
		Core:        true,
	}
}

func (m *SessionSearch) Tools(workdir string) []tools.Tool {
	return []tools.Tool{&tools.SessionSearch{}}
}
