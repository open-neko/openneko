package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCreatesDefault(t *testing.T) {
	dir := t.TempDir()
	s, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Marketplaces) != 1 || !s.Marketplaces[0].Official {
		t.Fatalf("expected official-only default, got %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dir, Filename)); err != nil {
		t.Fatalf("expected file to be created: %v", err)
	}
}

func TestReadInjectsOfficial(t *testing.T) {
	dir := t.TempDir()
	// Write a store without official to test re-injection.
	if err := Write(Store{Marketplaces: []TrustedMarketplace{
		{Name: "thirdparty", URL: "https://example.test/m.json", AddedAt: "2025-01-01"},
	}}, dir); err != nil {
		t.Fatal(err)
	}
	s, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Marketplaces) != 2 || s.Marketplaces[0].Name != "official" {
		t.Fatalf("expected official injected at head, got %+v", s)
	}
}

func TestAddDuplicateName(t *testing.T) {
	s := Store{Marketplaces: []TrustedMarketplace{{Name: "x", URL: "u1"}}}
	if _, err := Add(s, TrustedMarketplace{Name: "x", URL: "u2"}); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestAddDuplicateURL(t *testing.T) {
	s := Store{Marketplaces: []TrustedMarketplace{{Name: "x", URL: "u"}}}
	if _, err := Add(s, TrustedMarketplace{Name: "y", URL: "u"}); err == nil {
		t.Fatal("expected duplicate-url error")
	}
}

func TestRemoveOfficial(t *testing.T) {
	s := Store{Marketplaces: []TrustedMarketplace{{Name: "official", URL: "u", Official: true}}}
	if _, _, err := Remove(s, "official"); err == nil {
		t.Fatal("removing official should error")
	}
}

func TestRemoveByNameOrURL(t *testing.T) {
	s := Store{Marketplaces: []TrustedMarketplace{
		{Name: "official", URL: "uo", Official: true},
		{Name: "third", URL: "u3"},
	}}
	got, removed, err := Remove(s, "u3")
	if err != nil || removed == nil || removed.Name != "third" {
		t.Fatalf("expected removal of third via URL, got %v %v %v", got, removed, err)
	}
	if len(got.Marketplaces) != 1 {
		t.Fatalf("expected 1 remaining, got %v", got.Marketplaces)
	}
}

func TestRemoveMiss(t *testing.T) {
	s := Store{Marketplaces: []TrustedMarketplace{{Name: "x", URL: "u"}}}
	_, removed, err := Remove(s, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if removed != nil {
		t.Fatal("expected nil removed for miss")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My Marketplace":           "my-marketplace",
		"  weird!!! Stuff   ":      "weird-stuff",
		"123":                      "123",
		"this-is-a-very-long-name-that-exceeds-the-forty-char-cap": "this-is-a-very-long-name-that-exceeds-th",
		"---":                      "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Fatalf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}
