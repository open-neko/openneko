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
