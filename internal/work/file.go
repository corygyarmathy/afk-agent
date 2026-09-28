package work

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ReadGitFile is a file a session wrote in the workspace's .git, or nothing.
// Only a regular file is read: the .git is the model's to write, and a link
// there would put whatever it points at on the tracker.
func ReadGitFile(ws, name string) (string, error) {
	root, err := os.OpenRoot(filepath.Join(ws, ".git"))
	if errors.Is(err, fs.ErrNotExist) {
		// The workspace went with the run. The gate is what says so.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer root.Close()
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", nil
	}
	b, err := root.ReadFile(name)
	return string(b), err
}
