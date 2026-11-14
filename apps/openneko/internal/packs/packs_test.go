package packs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type nodeFixture struct {
	Canonical []struct {
		Value any    `json:"value"`
		JSON  string `json:"json"`
	} `json:"canonical"`
	Sort []string `json:"sort"`
	YAML []struct {
		Source string `json:"source"`
		JSON   string `json:"json"`
	} `json:"yaml"`
	Manifests []struct {
		Input      any    `json:"input"`
		OK         bool   `json:"ok"`
		Normalized string `json:"normalized"`
	} `json:"manifests"`
	Packs []struct {
		Dir          string `json:"dir"`
		Manifest     string `json:"manifest"`
		ManifestHash string `json:"manifestHash"`
		BundleHash   string `json:"bundleHash"`
		FilesHash    string `json:"filesHash"`
		Artifacts    []struct {
			Kind      ArtifactKind `json:"kind"`
			Key       string       `json:"key"`
			TargetRef string       `json:"targetRef"`
			Path      string       `json:"path"`
			Hash      string       `json:"hash"`
		} `json:"artifacts"`
	} `json:"packs"`
	Upload struct {
		ID          string `json:"id"`
		BundleHash  string `json:"bundleHash"`
		ContentHash string `json:"contentHash"`
	} `json:"upload"`
	Plans []struct {
		Name      string `json:"name"`
		Installed []struct {
			Kind            ArtifactKind `json:"kind"`
			Key             string       `json:"key"`
			TargetRef       string       `json:"targetRef"`
			DesiredHash     string       `json:"desiredHash"`
			LastAppliedHash string       `json:"lastAppliedHash"`
			CurrentHash     *string      `json:"currentHash"`
			Ownership       string       `json:"ownership"`
		} `json:"installed"`
		Plan json.RawMessage `json:"plan"`
	} `json:"plans"`
}

func loadFixture(t *testing.T) nodeFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/nodepacks.json")
	if err != nil {
		t.Fatal(err)
	}
	var f nodeFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func repoPath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "..", ".."}, parts...)...)
}

func TestCanonicalSortAndYAMLMatchNode(t *testing.T) {
	f := loadFixture(t)
	for _, c := range f.Canonical {
		got, err := CanonicalJSON(c.Value)
		if err != nil || got != c.JSON {
			t.Errorf("canonical %v:\n got %s\nwant %s (%v)", c.Value, got, c.JSON, err)
		}
	}
	shuffled := slices.Clone(f.Sort)
	slices.Reverse(shuffled)
	slices.SortFunc(shuffled, localeCompare)
	if !slices.Equal(shuffled, f.Sort) {
		t.Errorf("sort:\n got %q\nwant %q", shuffled, f.Sort)
	}
	for _, y := range f.YAML {
		v, err := parseYAML([]byte(y.Source))
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := CanonicalJSON(v); got != y.JSON {
			t.Errorf("yaml %q:\n got %s\nwant %s", y.Source, got, y.JSON)
		}
	}
}

func TestManifestChecksMatchNode(t *testing.T) {
	for i, m := range loadFixture(t).Manifests {
		parsed, err := ParseManifest(m.Input)
		if (err == nil) != m.OK {
			t.Errorf("manifest %d: ok=%v, Node ok=%v (%v)", i, err == nil, m.OK, err)
			continue
		}
		if err == nil {
			if got, _ := CanonicalJSON(parsed.Normalized()); got != m.Normalized {
				t.Errorf("manifest %d:\n got %s\nwant %s", i, got, m.Normalized)
			}
		}
	}
}

func TestBuiltinPacksHashLikeNode(t *testing.T) {
	f := loadFixture(t)
	if len(f.Packs) < 6 {
		t.Fatalf("fixture holds %d packs", len(f.Packs))
	}
	for _, want := range f.Packs {
		root := repoPath("packs", want.Dir)
		b, err := LoadBundle(root)
		if err != nil {
			t.Fatalf("%s: %v", want.Dir, err)
		}
		if got, _ := CanonicalJSON(b.Manifest.Normalized()); got != want.Manifest {
			t.Errorf("%s manifest:\n got %s\nwant %s", want.Dir, got, want.Manifest)
		}
		if b.ManifestHash != want.ManifestHash || b.BundleHash != want.BundleHash {
			t.Errorf("%s hashes: got %s %s, want %s %s", want.Dir, b.ManifestHash, b.BundleHash, want.ManifestHash, want.BundleHash)
		}
		if len(b.Artifacts) != len(want.Artifacts) {
			t.Fatalf("%s: %d artifacts, want %d", want.Dir, len(b.Artifacts), len(want.Artifacts))
		}
		for i, a := range b.Artifacts {
			w := want.Artifacts[i]
			if a.Kind != w.Kind || a.Key != w.Key || a.TargetRef != w.TargetRef || a.Path != w.Path || a.Hash != w.Hash {
				t.Errorf("%s artifact %d: got %s %s %s %s %s, want %+v", want.Dir, i, a.Kind, a.Key, a.TargetRef, a.Path, a.Hash, w)
			}
		}
		if h, err := HashFiles(root); err != nil || h != want.FilesHash {
			t.Errorf("%s files hash: got %s (%v), want %s", want.Dir, h, err, want.FilesHash)
		}
	}
}

type zipEntry struct {
	name  string
	data  []byte
	mode  uint32
	crc   *uint32
	size  *uint32
	flags uint16
	raw   bool
}

func buildZip(t *testing.T, entries []zipEntry, deflate bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o100600
			if strings.HasSuffix(e.name, "/") {
				mode = 0o040700
			}
		}
		h := &zip.FileHeader{Name: e.name, CreatorVersion: 0x0314, ExternalAttrs: mode << 16, Flags: e.flags}
		if deflate && !strings.HasSuffix(e.name, "/") {
			h.Method = zip.Deflate
		}
		if e.crc != nil || e.size != nil || e.flags != 0 {
			h.Method = zip.Store
			h.CRC32 = crc32.ChecksumIEEE(e.data)
			if e.crc != nil {
				h.CRC32 = *e.crc
			}
			h.CompressedSize64 = uint64(len(e.data))
			h.UncompressedSize64 = uint64(len(e.data))
			if e.size != nil {
				h.UncompressedSize64 = uint64(*e.size)
			}
			fw, err := w.CreateRaw(h)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fw.Write(e.data)
			continue
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(e.data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serviceHealthEntries(t *testing.T) []zipEntry {
	t.Helper()
	src := repoPath("apps", "worker", "test", "fixtures", "service-health")
	var entries []zipEntry
	var walk func(dir, prefix string)
	walk = func(dir, prefix string) {
		entries = append(entries, zipEntry{name: prefix + "/"})
		list, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range list {
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()), prefix+"/"+e.Name())
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, zipEntry{name: prefix + "/" + e.Name(), data: data})
		}
	}
	walk(src, "service-health")
	for _, kind := range []string{"actions", "policies"} {
		if !slices.ContainsFunc(entries, func(e zipEntry) bool { return e.name == "service-health/"+kind+"/" }) {
			entries = append(entries, zipEntry{name: "service-health/" + kind + "/"})
		}
	}
	return entries
}

func emptyDir(t *testing.T, dir string) {
	t.Helper()
	list, _ := os.ReadDir(dir)
	if len(list) != 0 {
		t.Fatalf("staging folder holds %d entries", len(list))
	}
}

func TestStageArchiveHashesLikeNode(t *testing.T) {
	f := loadFixture(t)
	root := t.TempDir()
	entries := serviceHealthEntries(t)
	for _, deflate := range []bool{false, true} {
		b, cleanup, err := StageArchive(context.Background(), buildZip(t, entries, deflate), root, nil)
		if err != nil {
			t.Fatal(err)
		}
		content, err := HashFiles(b.Root)
		if err != nil {
			t.Fatal(err)
		}
		if b.BundleHash != f.Upload.BundleHash || content != f.Upload.ContentHash {
			t.Errorf("deflate=%v: got %s %s, want %s %s", deflate, b.BundleHash, content, f.Upload.BundleHash, f.Upload.ContentHash)
		}
		info, _ := os.Stat(filepath.Join(b.Root, "pack.yaml"))
		if info.Mode().Perm() != 0o600 {
			t.Errorf("pack.yaml mode %o", info.Mode().Perm())
		}
		cleanup()
	}
	emptyDir(t, root)
}

func TestStageArchiveRejectsUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	base := serviceHealthEntries(t)
	one := uint32(1)
	huge := uint32(limitFile + 1)
	bad := [][]zipEntry{
		{{name: "../outside.md", data: []byte("no")}}, {{name: "/absolute.md"}}, {{name: "service-health/../outside.md"}},
		{{name: `service-health\outside.md`}}, {{name: "other/file.md"}},
		{{name: "service-health/link.md", mode: 0o120777, data: []byte("../../outside")}},
		{{name: "service-health/fifo.md", mode: 0o010600}},
		{{name: "service-health/run.md", mode: 0o100755, data: []byte("executable")}},
		{{name: "service-health/run.md", data: []byte("#!/bin/sh\nexit 0")}},
		{{name: "service-health/nested.zip", data: []byte("PK")}}, {{name: "service-health/nested.md", data: []byte("PK archive")}},
		{{name: "service-health/binary.md", data: []byte{0, 255}}},
		{{name: "service-health/duplicate.md"}, {name: "service-health/DUPLICATE.md"}},
		{{name: "service-health/Folder/a.md"}, {name: "service-health/folder/b.md"}},
		{{name: "service-health/file.md"}, {name: "service-health/file.md/child.md"}},
		{{name: "service-health/encrypted.md", flags: 1}},
		{{name: "service-health/corrupt.md", data: []byte("hello"), crc: &one}},
		{{name: "service-health/huge.md", data: []byte("x"), size: &huge}},
		{{name: "service-health/" + strings.Repeat("deep/", 17) + "file.md"}},
	}
	for i, extra := range bad {
		if _, _, err := StageArchive(context.Background(), buildZip(t, append(slices.Clone(base), extra...), false), root, nil); err == nil {
			t.Errorf("case %d (%s) was accepted", i, extra[0].name)
		}
		emptyDir(t, root)
	}
	bomb := buildZip(t, append(slices.Clone(base), zipEntry{name: "service-health/bomb.md", data: []byte(strings.Repeat("x", 100_000))}), true)
	if _, _, err := StageArchive(context.Background(), bomb, root, nil); err == nil || !strings.Contains(err.Error(), "ratio") {
		t.Errorf("bomb: %v", err)
	}
	checks := []struct {
		data     []byte
		reserved []string
		want     string
	}{
		{[]byte("not a zip"), nil, "invalid pack ZIP"},
		{buildZip(t, nil, false), nil, "empty"},
		{buildZip(t, base, false), []string{"service-health"}, "reserved"},
		{make([]byte, limitCompressed+1), nil, "16 MiB"},
	}
	for _, c := range checks {
		if _, _, err := StageArchive(context.Background(), c.data, root, c.reserved); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
	many := slices.Clone(base)
	for i := range 1001 {
		many = append(many, zipEntry{name: "service-health/" + itoa(i) + ".md"})
	}
	if _, _, err := StageArchive(context.Background(), buildZip(t, many, false), root, nil); err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Errorf("many: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := StageArchive(ctx, buildZip(t, base, false), root, nil); err == nil {
		t.Error("a cancelled upload was accepted")
	}
	emptyDir(t, root)
}

func TestStoreUploadIsImmutableAndLoads(t *testing.T) {
	root := t.TempDir()
	actor := "user-1"
	data := buildZip(t, serviceHealthEntries(t), false)
	first, err := StoreUpload(context.Background(), root, "org-1", data, nil, &actor)
	if err != nil {
		t.Fatal(err)
	}
	again, err := StoreUpload(context.Background(), root, "org-1", buildZip(t, serviceHealthEntries(t), true), nil, nil)
	if err != nil || again.UploadedAt != first.UploadedAt || *again.UploadedBy != actor {
		t.Fatalf("second upload of the same content: %+v %v", again, err)
	}
	changed := serviceHealthEntries(t)
	changed = append(changed, zipEntry{name: "service-health/notes.md", data: []byte("new")})
	if _, err := StoreUpload(context.Background(), root, "org-1", buildZip(t, changed, false), nil, nil); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("changed content: %v", err)
	}
	listed, err := ListUploaded(root, "org-1")
	if err != nil || len(listed) != 1 || listed[0].Upload.ContentHash != first.ContentHash {
		t.Fatalf("list: %v %v", listed, err)
	}
	if _, err := LoadUploaded(root, "org-2", "service-health", ""); err == nil {
		t.Fatal("another organization loaded the upload")
	}
	if err := os.WriteFile(filepath.Join(root, "service-health", "versions", "0.1.0", "bundle", "pack.yaml"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUploaded(root, "org-1", "service-health", ""); err == nil || !strings.Contains(err.Error(), "changed after upload") {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := StoreUpload(context.Background(), root, "org-1", data, []string{"service-health"}, nil); err == nil {
		t.Fatal("a reserved id was stored")
	}
}

func TestPlanMatchesNode(t *testing.T) {
	f := loadFixture(t)
	b, cleanup, err := StageArchive(context.Background(), buildZip(t, serviceHealthEntries(t), false), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, c := range f.Plans {
		installed := make([]InstalledArtifact, len(c.Installed))
		for i, a := range c.Installed {
			installed[i] = InstalledArtifact{Kind: a.Kind, Key: a.Key, TargetRef: a.TargetRef, DesiredHash: a.DesiredHash,
				LastAppliedHash: a.LastAppliedHash, CurrentHash: a.CurrentHash, Ownership: a.Ownership}
		}
		got, _ := json.Marshal(PlanBundle(b, installed))
		var gotV, wantV any
		_ = json.Unmarshal(got, &gotV)
		_ = json.Unmarshal(c.Plan, &wantV)
		g, _ := CanonicalJSON(gotV)
		w, _ := CanonicalJSON(wantV)
		if g != w {
			t.Errorf("plan %s:\n got %s\nwant %s", c.Name, g, w)
		}
	}
}

func TestPermissions(t *testing.T) {
	b, err := LoadBundle(repoPath("packs", "slack"))
	if err != nil {
		t.Fatal(err)
	}
	got := Permissions(b)
	if got["network"] != "slack.com" || got["database"] != "none" {
		t.Fatalf("slack permissions: %v", got)
	}
}
