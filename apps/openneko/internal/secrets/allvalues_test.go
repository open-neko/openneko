package secrets

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

func TestAllValuesFullReadsSectionsAndOperatorTokens(t *testing.T) {
	dir := t.TempDir()
	sealedSection, err := config.EncryptValue(dir, "section-sealed-value")
	if err != nil {
		t.Fatal(err)
	}
	sealedCred, err := config.EncryptValue(dir, `{"tokens":{"access_token":"sealed-access-token","nested":{"list":["sealed-list-token"]}},"connectedAt":"2026-11-14T00:00:00Z"}`)
	if err != nil {
		t.Fatal(err)
	}
	file := map[string]any{
		"pack.shop": map[string]any{"API_KEY": "plain-section-value", "SEALED": sealedSection, "EMPTY": ""},
		OperatorsKey: map[string]any{
			"op-1": map[string]any{
				"sealed": sealedCred,
				"plain":  map[string]any{"tokens": map[string]any{"refresh_token": "plain-refresh-token"}, "connectedAt": "2026-11-14T00:00:00Z"},
				"broken": map[string]any{"tokens": map[string]any{"refresh_token": "no-connected-at"}},
			},
		},
	}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(dir), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := AllValuesFull(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"plain-refresh-token", "plain-section-value", "sealed-access-token", "sealed-list-token", "section-sealed-value"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAllValuesFullWithNoFile(t *testing.T) {
	got, err := AllValuesFull(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}
