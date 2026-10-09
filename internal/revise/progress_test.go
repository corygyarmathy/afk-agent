package revise_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// A progress file the code before delivery wrote (testdata, #212), with a
// correction and without, is one an upgraded agent carries on from: the
// hand-off runs from it, keyed by the head it names, and saving it again gives
// the same keys and values. Only their order may differ.
func TestAProgressFileFromBeforeDeliveryIsCarriedOn(t *testing.T) {
	for _, name := range []string{"progress.json", "progress-correction.json"} {
		t.Run(name, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			f := setup(t)
			// The claim took the hand-off label off, as it does before the work.
			f.tr.PullRequests[12].Labels = []string{"bug"}
			ctx := context.Background()
			job, err := f.store.Ensure(ctx, store.KindRevise, store.Subject{Type: store.SubjectPR, Number: 12}, revise.HandingOff, now)
			if err != nil {
				t.Fatal(err)
			}
			path := work.Workspace{StateDir: f.deps.StateDir}.ProgressPath(job.ID)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, golden, 0o644); err != nil {
				t.Fatal(err)
			}

			out, err := f.run.Run(ctx, "revise-hand-off", job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(out.Performed, " "); got != "revise-hand-off-pr-12-2222222222222222222222222222222222222222-0" {
				t.Errorf("performed [%s], want the hand-off keyed by the pushed head the file names", got)
			}
			if labels := f.tr.PullRequests[12].Labels; !slices.Contains(labels, handOff) {
				t.Errorf("labels %v, want the hand-off label", labels)
			}

			if err := revise.RoundTrip(f.deps, job.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, saved, golden)
		})
	}
}

// sameJSON fails t unless got and want are the same JSON, whatever the order of
// their keys.
func sameJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("saved again:\n%s\nwant the same keys and values as:\n%s", got, want)
	}
}
