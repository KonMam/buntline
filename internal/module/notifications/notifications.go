// Package notifications is the notifications feature module: cross-session
// in-app alerts and desktop popups. The module itself carries no routes,
// tools, or state. The bell, the attention banner, and the settings view
// are browser-side, and settings persist per browser in localStorage. But
// registering it makes the feature visible and toggleable in the modules
// store: disabling it stops the browser from subscribing to the global
// event stream, so a disabled module truly costs nothing.
package notifications

import "github.com/KonMam/buntline/internal/module"

// Module is the notifications feature. Purely presentational: no routes,
// no tools, nothing to stop.
type Module struct{}

func (Module) Info() module.Info {
	return module.Info{
		ID:          "notifications",
		Name:        "Notifications",
		Description: "Cross-session alerts: approvals, questions, turn ends, and errors from every session, in the bell and as desktop popups.",
		Default:     true,
	}
}
