package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// What the application remembers between sessions — the settings and the
// recent repositories — lives in one directory under the user's configuration
// directory. It is named here and nowhere else.
//
// Nothing about a review is kept: read marks and notes live in memory for as
// long as the window is open. See the Store.
const dirName = "check"

// dir returns the application's configuration directory, creating it if it is
// not there.
func dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, dirName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// PlansDir is where written plans are kept, one file per change and model,
// so that a plan is paid for once.
func PlansDir() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(d, "plans")
	return path, os.MkdirAll(path, 0o755)
}

// writeJSON writes a value to a file in the application's own directory,
// atomically: the bytes land in a temporary file that is then renamed over the
// old one, so a crash part way through leaves the previous file intact rather
// than a half-written one.
func writeJSON(name string, v any) error {
	path, err := file(name)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Readable by the owner alone: the settings carry an API key. A stale
	// temporary file is removed first, since WriteFile keeps the mode of a
	// file that is already there.
	tmp := path + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readJSON reads a value back, reporting whether there was anything to read.
// A missing or unreadable file is the ordinary case on a first run, and not an
// error worth passing on.
func readJSON(name string, v any) bool {
	path, err := file(name)
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

// file returns the path of a file in the application's own directory.
func file(name string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}
