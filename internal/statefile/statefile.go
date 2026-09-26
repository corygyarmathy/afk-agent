// Package statefile keeps a value as JSON in a file in the state directory,
// beside the store and never in it (ADR 0001 §5).
package statefile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Save writes v to path atomically, so a crash leaves the old file or the new
// one and never half of one.
func Save(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads path into v. A file that is not there is os.ErrNotExist to
// errors.Is. Whether what it read is complete is the caller's to say.
func Load(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
