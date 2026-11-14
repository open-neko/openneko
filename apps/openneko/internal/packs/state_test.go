package packs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type nodeStateFixture struct {
	Files  map[string]string `json:"files"`
	Native []struct {
		Kind ArtifactKind   `json:"kind"`
		Row  map[string]any `json:"row"`
		Hash string         `json:"hash"`
	} `json:"native"`
	Policy struct {
		Row           map[string]any `json:"row"`
		LegacyReceipt string         `json:"legacyReceipt"`
		Current       string         `json:"current"`
		WithLegacy    string         `json:"withLegacy"`
		WithOther     string         `json:"withOther"`
	} `json:"policy"`
	LiveSources []struct {
		Source map[string]any `json:"source"`
		Hash   string         `json:"hash"`
	} `json:"liveSources"`
	LiveSourceMiss string `json:"liveSourceMiss"`
	Locators       []struct {
		Artifact Artifact       `json:"artifact"`
		Locator  map[string]any `json:"locator"`
	} `json:"locators"`
	State []struct {
		Name      string         `json:"name"`
		Kind      ArtifactKind   `json:"kind"`
		TargetRef string         `json:"targetRef"`
		Metadata  map[string]any `json:"metadata"`
		Hash      *string        `json:"hash"`
		Error     *string        `json:"error"`
	} `json:"state"`
}

func loadStateFixture(t *testing.T) nodeStateFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/nodestate.json")
	if err != nil {
		t.Fatal(err)
	}
	var f nodeStateFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestStateHashesMatchNode(t *testing.T) {
	f := loadStateFixture(t)
	for _, n := range f.Native {
		if got, err := NativeStateHash(n.Kind, n.Row); err != nil || got != n.Hash {
			t.Errorf("%s: got %s %v, want %s", n.Kind, got, err, n.Hash)
		}
	}
	p := f.Policy
	for _, c := range []struct{ last, want string }{{"", p.Current}, {p.LegacyReceipt, p.WithLegacy}, {"other", p.WithOther}} {
		if got, _ := PolicyStateHash(p.Row, c.last); got != c.want {
			t.Errorf("policy with %q: got %s, want %s", c.last, got, c.want)
		}
	}
	for _, s := range f.LiveSources {
		if got, _ := LiveSourceStateHash(s.Source, ""); got != s.Hash {
			t.Errorf("live source %v: got %s, want %s", s.Source["name"], got, s.Hash)
		}
	}
	if got, _ := LiveSourceStateHash(f.LiveSources[0].Source, "legacy-miss"); got != f.LiveSourceMiss {
		t.Errorf("live source miss: got %s, want %s", got, f.LiveSourceMiss)
	}
	for _, l := range f.Locators {
		got, _ := json.Marshal(Locator(l.Artifact))
		want, _ := json.Marshal(l.Locator)
		if string(got) != string(want) {
			t.Errorf("locator %s: got %s, want %s", l.Artifact.Key, got, want)
		}
	}
}

func TestInspectInstalledMatchesNode(t *testing.T) {
	f := loadStateFixture(t)
	root := t.TempDir()
	for path, text := range f.Files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := &GraphjinTarget{Mode: "files", ConfigFile: filepath.Join(root, "graphjin.yml")}
	for _, c := range f.State {
		metadata := c.Metadata
		if m, ok := metadata["materializedTarget"].(string); ok && !strings.HasPrefix(m, "graphjin:") {
			metadata = map[string]any{"materializedTarget": filepath.Join(root, filepath.FromSlash(m))}
		}
		got, err := InspectInstalled(context.Background(), nil, StateInput{Kind: c.Kind, TargetRef: c.TargetRef, Metadata: metadata, Graphjin: target})
		switch {
		case c.Error != nil:
			prefix, _, _ := strings.Cut(*c.Error, ": ")
			if err == nil || !strings.HasPrefix(err.Error(), prefix+": ") {
				t.Errorf("%s: got %v, want error %q", c.Name, err, *c.Error)
			}
		case err != nil:
			t.Errorf("%s: %v", c.Name, err)
		case (got == nil) != (c.Hash == nil) || (got != nil && *got != *c.Hash):
			t.Errorf("%s: got %v, want %v", c.Name, deref(got), deref(c.Hash))
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestInspectInstalledOnTheAPI(t *testing.T) {
	f := loadStateFixture(t)
	sources := []any{}
	for _, s := range f.LiveSources {
		sources = append(sources, s.Source)
	}
	query := "query Top {\n  orders { id }\n}"
	target := &GraphjinTarget{
		Mode: "api",
		Live: func(context.Context) (map[string]any, error) { return map[string]any{"sources": sources}, nil },
		SavedQuery: func(_ context.Context, name string) (*string, error) {
			if name == "top" {
				return &query, nil
			}
			return nil, nil
		},
	}
	ctx := context.Background()
	got, err := InspectInstalled(ctx, nil, StateInput{Kind: KindSource, TargetRef: "shop", Graphjin: target})
	if err != nil || got == nil || *got != f.LiveSources[0].Hash {
		t.Fatalf("api source: %v %v", deref(got), err)
	}
	got, _ = InspectInstalled(ctx, nil, StateInput{Kind: KindSavedQuery, Metadata: map[string]any{"materializedTarget": "graphjin:saved_query:top"}, Graphjin: target})
	if want := SHA256([]byte(query)); got == nil || *got != want {
		t.Fatalf("api saved query: %v", deref(got))
	}
	got, _ = InspectInstalled(ctx, nil, StateInput{Kind: KindSpec, Metadata: map[string]any{"materializedTarget": "graphjin:spec:specs/Shop/shop.YAML"}, Graphjin: target})
	if want, _ := CanonicalHash(map[string]any{"x": float64(1)}); got == nil || *got != want {
		t.Fatalf("api spec: %v", deref(got))
	}
	got, _ = InspectInstalled(ctx, nil, StateInput{Kind: KindSpec, Metadata: map[string]any{"materializedTarget": "specs/shop.yaml"}, Graphjin: target})
	if got != nil {
		t.Fatalf("a file target on the api must read as missing: %v", *got)
	}
}

func TestInspectInstalledNativeRows(t *testing.T) {
	row := map[string]any{"name": "Stock", "enabled": true, "updated_at": "2026-01-01"}
	var asked map[string]any
	lookup := func(_ context.Context, kind ArtifactKind, locator map[string]any, targetRef string) (map[string]any, error) {
		asked = map[string]any{"kind": string(kind), "name": LocatorValue(locator, "name", targetRef)}
		return row, nil
	}
	fallback := Artifact{Kind: KindWatcher, Key: "w", TargetRef: "stock", Content: map[string]any{"name": "Stock"}}
	got, err := InspectInstalled(context.Background(), lookup, StateInput{Kind: KindWatcher, TargetRef: "stock", Metadata: map[string]any{}, Fallback: &fallback})
	want, _ := NativeStateHash(KindWatcher, map[string]any{"name": "Stock", "enabled": true})
	if err != nil || got == nil || *got != want || asked["name"] != "Stock" {
		t.Fatalf("native: %v %v %v", deref(got), err, asked)
	}
}
