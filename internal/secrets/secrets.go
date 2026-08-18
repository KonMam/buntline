// Package secrets stores API keys entered through the UI. On macOS they
// live in the login Keychain (via the security CLI, consistent with the
// project's shell-out stance); elsewhere in a 0600 JSON file. Values are
// write-only from the API's perspective: endpoints report which names are
// set, never what they contain.
package secrets

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const service = "tether"

var mu sync.Mutex

func filePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tether", "secrets.json")
}

func useKeychain() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("security")
	return err == nil
}

// Backend names the storage in use, for the UI.
func Backend() string {
	if useKeychain() {
		return "macOS Keychain"
	}
	return "~/.config/tether/secrets.json"
}

// Get returns the stored value for name, or "".
func Get(name string) string {
	if name == "" {
		return ""
	}
	if useKeychain() {
		out, err := exec.Command("security", "find-generic-password",
			"-s", service+":"+name, "-w").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	m, _ := readFile()
	return m[name]
}

// Set stores (or with an empty value removes) a secret and records the
// name in the index so List works with the Keychain backend too.
func Set(name, value string) error {
	mu.Lock()
	defer mu.Unlock()
	if name == "" || strings.ContainsAny(name, " \t\n") {
		return fmt.Errorf("invalid secret name %q", name)
	}
	if value == "" {
		return remove(name)
	}
	if useKeychain() {
		// -U updates in place if the item exists.
		if err := exec.Command("security", "add-generic-password",
			"-U", "-a", service, "-s", service+":"+name, "-w", value).Run(); err != nil {
			return fmt.Errorf("keychain: %w", err)
		}
		return indexAdd(name)
	}
	m, _ := readFile()
	if m == nil {
		m = map[string]string{}
	}
	m[name] = value
	return writeFile(m)
}

// List returns the names of stored secrets, sorted.
func List() []string {
	mu.Lock()
	defer mu.Unlock()
	var names []string
	if useKeychain() {
		names = indexRead()
	} else {
		m, _ := readFile()
		for k := range m {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

func remove(name string) error {
	if useKeychain() {
		_ = exec.Command("security", "delete-generic-password",
			"-s", service+":"+name).Run() // absent item is fine
		return indexRemove(name)
	}
	m, _ := readFile()
	delete(m, name)
	return writeFile(m)
}

// --- keychain name index (names only, never values) ---

func indexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tether", "secret-names.json")
}

func indexRead() []string {
	data, err := os.ReadFile(indexPath())
	if err != nil {
		return nil
	}
	var names []string
	_ = json.Unmarshal(data, &names)
	return names
}

func indexWrite(names []string) error {
	sort.Strings(names)
	data, _ := json.Marshal(names)
	if err := os.MkdirAll(filepath.Dir(indexPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(indexPath(), data, 0o600)
}

func indexAdd(name string) error {
	names := indexRead()
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return indexWrite(append(names, name))
}

func indexRemove(name string) error {
	names := indexRead()
	out := names[:0]
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return indexWrite(out)
}

// --- file backend ---

func readFile() (map[string]string, error) {
	data, err := os.ReadFile(filePath())
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeFile(m map[string]string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filePath(), data, 0o600)
}
