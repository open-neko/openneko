package assets

import (
	"bytes"
	"os"
	"testing"
)

func TestGraphJinSeedMatchesTheRepoTemplate(t *testing.T) {
	raw, err := os.ReadFile("../../../db/graphjin/customer.sources.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, GraphJinSeed) {
		t.Fatal("assets/graphjin/customer.sources.yml differs from db/graphjin/customer.sources.example.yml; copy it again")
	}
}

func TestAdventureWorksInstallSQLMatchesTheRepoTemplate(t *testing.T) {
	raw, err := os.ReadFile("../../../db/seeds/dev/adventureworks-install.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, AdventureWorksInstallSQL) {
		t.Fatal("assets/adventureworks/install.sql differs from db/seeds/dev/adventureworks-install.sql; copy it again")
	}
}
