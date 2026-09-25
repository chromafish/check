package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// file is where the plan of a commit by a model is kept in dir.
func file(dir, commit, model string) string {
	sum := sha256.Sum256([]byte(commit + "\x00" + model))
	return filepath.Join(dir, hex.EncodeToString(sum[:12])+".json")
}

// Save keeps a plan, so the same change asked of the same model is not paid
// for twice.
func Save(dir string, p *Plan) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	path := file(dir, p.Commit, p.Model)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load is a plan kept for a commit by a model, if there is one.
func Load(dir, commit, model string) (*Plan, bool) {
	raw, err := os.ReadFile(file(dir, commit, model))
	if err != nil {
		return nil, false
	}
	var p Plan
	if json.Unmarshal(raw, &p) != nil || p.Commit != commit {
		return nil, false
	}
	return &p, true
}
