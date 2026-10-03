package work_test

import (
	"strings"
	"testing"
	"text/template"

	"github.com/corygyarmathy/afk-agent/internal/work"
)

// Every kind that commits is told the same three things: nobody is there to
// ask, which branch to commit to and that it stays on it, and that the agent
// runs the gate. A review, which commits nothing, takes the first alone.
func TestTheUnattendedPartSaysWhatEveryKindIsTold(t *testing.T) {
	tmpl := template.Must(template.New("unattended").Parse(work.Unattended))
	var all, nobody strings.Builder
	if err := tmpl.Execute(&all, struct{ Branch, Gate string }{"afk/7-1", "make check"}); err != nil {
		t.Fatal(err)
	}
	if err := tmpl.ExecuteTemplate(&nobody, "nobody", nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"No one is in the session", "`afk/7-1`", "switch or delete", "`make check`"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("the unattended part does not say %q:\n%s", want, all.String())
		}
	}
	if !strings.Contains(all.String(), nobody.String()) || strings.Contains(nobody.String(), "afk/7-1") {
		t.Errorf("the nobody part is not the unattended part's first line alone:\n%s", nobody.String())
	}
}
