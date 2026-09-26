package statefile_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/statefile"
)

type record struct {
	Issue int
	Head  string
	Tries []int
}

// What was saved is what comes back, including into a directory that did not
// exist yet: the state directory's subdirectories are made on first save.
func TestALoadReturnsWhatWasSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs", "7.json")
	want := record{Issue: 73, Head: "2eed384", Tries: []int{1, 2}}

	if err := statefile.Save(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	var got record
	if err := statefile.Load(path, &got); err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded %+v, saved %+v", got, want)
	}
}

// A file that is not there is os.ErrNotExist to errors.Is, which is how
// callers tell "nothing saved yet" from a file they could not read.
func TestAMissingFileIsNotExist(t *testing.T) {
	var got record
	err := statefile.Load(filepath.Join(t.TempDir(), "absent.json"), &got)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loading a missing file: %v, want os.ErrNotExist", err)
	}
}

// A save that fails leaves the file it would have replaced, whole. That holds
// whether the value will not encode or the value encodes and the write fails.
func TestAFailedSaveKeepsThePreviousFile(t *testing.T) {
	for name, fail := range map[string]func(t *testing.T, path string) any{
		// A value JSON cannot encode fails before anything is written.
		"the value will not encode": func(t *testing.T, path string) any {
			return struct{ C chan int }{make(chan int)}
		},
		// A directory where the temporary file goes fails the write itself.
		"the write fails": func(t *testing.T, path string) any {
			if err := os.Mkdir(path+".tmp", 0o755); err != nil {
				t.Fatal(err)
			}
			return record{Issue: 2, Head: "new"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "7.json")
			old := record{Issue: 1, Head: "old", Tries: []int{1}}
			if err := statefile.Save(path, old); err != nil {
				t.Fatalf("first save: %v", err)
			}

			if err := statefile.Save(path, fail(t, path)); err == nil {
				t.Fatal("the save succeeded, want it to fail")
			}

			var got record
			if err := statefile.Load(path, &got); err != nil {
				t.Fatalf("load after a failed save: %v", err)
			}
			if !reflect.DeepEqual(got, old) {
				t.Fatalf("after a failed save the file holds %+v, want %+v", got, old)
			}
		})
	}
}
