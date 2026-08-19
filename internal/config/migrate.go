package config

import (
	"os"
	"path/filepath"
)

// MigrateLegacyDirs renames the pre-rebrand tether directories to their
// buntline successors, so an upgraded install keeps its providers, keys,
// and sessions instead of silently starting from zero (the rebrand
// orphaned them once; this makes the rename a non-event for users). A
// rename only happens when the old directory exists and the new one does
// not: a buntline directory, however partial, is current state and is
// never overwritten. Called by main before Load, not by Load itself, so
// tests exercising Load never move real directories. Returns the moves
// made ("old -> new") for startup logging.
func MigrateLegacyDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	pairs := [][2]string{
		{filepath.Join(home, ".config", "tether"), filepath.Join(home, ".config", "buntline")},
		{filepath.Join(home, ".local", "share", "tether"), filepath.Join(home, ".local", "share", "buntline")},
	}
	var moved []string
	for _, p := range pairs {
		old, next := p[0], p[1]
		if _, err := os.Stat(old); err != nil {
			continue
		}
		if _, err := os.Stat(next); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(next), 0o755); err != nil {
			continue
		}
		if err := os.Rename(old, next); err == nil {
			moved = append(moved, old+" -> "+next)
		}
	}
	return moved
}
