package model_test

import (
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/model"
)

// The decode is asserted against the frozen excerpt rather than against a
// handwritten document, because the failure worth catching is upstream's shape
// differing from our idea of it, and a document we wrote cannot differ.
func TestDecodeCatalogue(t *testing.T) {
	cat := fixture(t)

	if cat.Len() != 10 {
		t.Fatalf("fixture has %d models, want 10", cat.Len())
	}

	tests := []struct {
		ref  string
		want model.Model
	}{
		{
			ref: "opencode-go/glm-5.3",
			want: model.Model{
				Name:             "GLM-5.3",
				ToolCall:         true,
				Reasoning:        true,
				StructuredOutput: true,
				InputModalities:  []string{"text"},
				OutputModalities: []string{"text"},
				Limit:            model.Limit{Context: 1_000_000, Output: 131072},
				Price:            model.Price{Input: 1.4, Output: 4.4, CacheRead: 0.26, Known: true},
			},
		},
		{
			// The reason the worst-band rule exists. grok-4.6 advertises 2/6
			// and, above 200k context, 4/12; both spellings upstream uses for
			// that - `tiers` and `context_over_200k` - are present on this
			// entry, and the decode must arrive at the dearer numbers whichever
			// one it read.
			ref: "opencode-go/grok-4.6",
			want: model.Model{
				Name:             "Grok 4.6",
				Attachment:       true,
				ToolCall:         true,
				Reasoning:        true,
				StructuredOutput: true,
				InputModalities:  []string{"text", "image"},
				OutputModalities: []string{"text"},
				Limit:            model.Limit{Context: 500_000, Output: 500_000},
				Price:            model.Price{Input: 4, Output: 12, CacheRead: 1, Known: true},
			},
		},
		{
			// Free is a price, and is not the same fact as unpriced.
			ref: "opencode-go/ox-alpha-free",
			want: model.Model{
				Name:             "Ox Alpha Free (Unlimited)",
				Attachment:       true,
				ToolCall:         true,
				Reasoning:        true,
				StructuredOutput: true,
				InputModalities:  []string{"text", "image", "video"},
				OutputModalities: []string{"text"},
				Limit:            model.Limit{Context: 1_000_000, Output: 131072},
				Price:            model.Price{Known: true},
			},
		},
		{
			// Several hundred entries upstream carry no cost block at all.
			// Known must be false, or every ceiling admits them.
			ref: "qiniu-ai/glm-4.5-air",
			want: model.Model{
				Name:             "GLM 4.5 Air",
				ToolCall:         true,
				Reasoning:        true,
				InputModalities:  []string{"text"},
				OutputModalities: []string{"text"},
				Limit:            model.Limit{Context: 131000, Output: 4096},
				Price:            model.Price{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got, ok := cat.Lookup(ref(t, tt.ref))
			if !ok {
				t.Fatalf("%s is not in the fixture", tt.ref)
			}
			tt.want.Ref = ref(t, tt.ref)
			if got.Name != tt.want.Name ||
				got.Attachment != tt.want.Attachment ||
				got.ToolCall != tt.want.ToolCall ||
				got.Reasoning != tt.want.Reasoning ||
				got.StructuredOutput != tt.want.StructuredOutput ||
				got.Limit != tt.want.Limit ||
				got.Price != tt.want.Price ||
				strings.Join(got.InputModalities, ",") != strings.Join(tt.want.InputModalities, ",") ||
				strings.Join(got.OutputModalities, ",") != strings.Join(tt.want.OutputModalities, ",") {
				t.Fatalf("\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// An empty document parses cleanly as JSON and would leave the resolver with
// nothing to choose among while nothing looked wrong. It is refused so that
// Source falls back to the last good copy instead.
func TestDecodeCatalogueRefusesEmpty(t *testing.T) {
	for _, body := range []string{`{}`, `{"opencode-go":{"models":{}}}`} {
		if _, err := model.DecodeCatalogue(strings.NewReader(body)); err == nil {
			t.Fatalf("%s decoded without error", body)
		}
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		in       string
		provider string
		model    string
		wantErr  bool
	}{
		{in: "opencode-go/glm-5.3", provider: "opencode-go", model: "glm-5.3"},
		// A model id may itself contain a slash; the split is on the first one
		// and the rest is the id, whatever is in it.
		{in: "orcarouter/qwen/qwen3.5-plus", provider: "orcarouter", model: "qwen/qwen3.5-plus"},
		{in: "glm-5.3", wantErr: true},
		{in: "/glm-5.3", wantErr: true},
		{in: "opencode-go/", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := model.ParseRef(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("%q parsed as %s", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Provider != tt.provider || got.Model != tt.model {
				t.Fatalf("got %q + %q", got.Provider, got.Model)
			}
			if got.String() != tt.in {
				t.Fatalf("round trip gave %q", got.String())
			}
		})
	}
}
