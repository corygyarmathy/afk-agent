package work

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
)

// Workspace is where a job's checkout, its relay and its progress live: paths
// on the host, and the remote they are made from. A value, built by the kind
// from its own dependencies.
//
// The state directory is beside the store and never in it (ADR 0001 §5), so a
// progress file without its workspace describes nothing, and a workspace
// without its progress cannot be trusted.
type Workspace struct {
	// StateDir is the directory workspaces and their progress live under.
	StateDir string

	// Remote is the repository a workspace is cloned from and the work is
	// pushed to, and the App's installation token every git process that
	// reaches it carries.
	Remote git.Remote
}

// Dir is the job's checkout.
func (w Workspace) Dir(jobID string) string {
	return filepath.Join(w.Workspaces(), jobID)
}

// Workspaces is the directory every job's checkout is under: what the remote
// is never reached from (git.Remote.Untrusted).
func (w Workspace) Workspaces() string {
	return filepath.Join(w.StateDir, "workspaces")
}

// RelayDir is the bare repository only the agent writes, which the push is
// made from.
func (w Workspace) RelayDir(jobID string) string {
	return filepath.Join(w.Relays(), jobID+".git")
}

// Relays is the directory every job's relay is under: outside the workspaces,
// so that the remote may be reached from it.
func (w Workspace) Relays() string {
	return filepath.Join(w.StateDir, "relays")
}

// ProgressPath is where the job's progress waits.
func (w Workspace) ProgressPath(jobID string) string {
	return filepath.Join(w.StateDir, "progress", jobID+".json")
}

// NotePath is where an effect's last error waits for the decision that reads it
// back (transition.Noting).
func (w Workspace) NotePath(jobID string) string {
	return filepath.Join(w.StateDir, "notes", jobID+".json")
}

// Exists reports whether the job's checkout is there with a .git in it.
func (w Workspace) Exists(jobID string) bool {
	return isDir(filepath.Join(w.Dir(jobID), ".git"))
}

// MakeDir makes the directory the job's checkout goes in.
func (w Workspace) MakeDir(jobID string) error {
	return os.MkdirAll(filepath.Dir(w.Dir(jobID)), 0o755)
}

// Save writes the job's progress, which is a struct embedding Progress.
func (w Workspace) Save(jobID string, p any) error {
	return statefile.Save(w.ProgressPath(jobID), p)
}

// Clear removes a job's workspace, its relay, its progress and its note. It
// refuses with no state directory, where the paths would be relative to
// wherever the process is.
func (w Workspace) Clear(jobID string) error {
	if err := w.Discard(jobID); err != nil {
		return err
	}
	for _, path := range []string{w.ProgressPath(jobID), w.NotePath(jobID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Discard removes a job's workspace and its relay, and leaves what describes
// the work.
func (w Workspace) Discard(jobID string) error {
	if w.StateDir == "" {
		return errors.New("work has no state directory")
	}
	if err := os.RemoveAll(w.Dir(jobID)); err != nil {
		return err
	}
	return os.RemoveAll(w.RelayDir(jobID))
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
