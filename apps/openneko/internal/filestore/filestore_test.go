package filestore_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dosco/graphjin/core/v3/fstable"

	"github.com/open-neko/neko/apps/openneko/internal/filestore"
	"github.com/open-neko/neko/apps/openneko/internal/filestore/s3test"
)

func TestOpenRefusesBadBucketSettings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cases := map[string]filestore.Config{
		"no s3 scheme":         {Dir: dir, Bucket: "files"},
		"no bucket name":       {Dir: dir, Bucket: "s3:///prefix"},
		"dot-dot prefix":       {Dir: dir, Bucket: "s3://files/a/../b"},
		"endpoint, no bucket":  {Dir: dir, Endpoint: "https://s3.example.com"},
		"key, no bucket":       {Dir: dir, AccessKeyID: "k", SecretKey: "s"},
		"key without secret":   {Dir: dir, Bucket: "s3://files", AccessKeyID: "k"},
		"secret without key":   {Dir: dir, Bucket: "s3://files", SecretKey: "s"},
		"http to another host": {Dir: dir, Bucket: "s3://files", Endpoint: "http://s3.example.com:9000"},
		"credentials in url":   {Dir: dir, Bucket: "s3://files", Endpoint: "https://u:p@s3.example.com"},
		"query in url":         {Dir: dir, Bucket: "s3://files", Endpoint: "https://s3.example.com/?x=1"},
		"other scheme":         {Dir: dir, Bucket: "s3://files", Endpoint: "ftp://s3.example.com"},
	}
	for name, c := range cases {
		if _, err := filestore.Open(ctx, c); err == nil {
			t.Errorf("%s: Open accepted %+v", name, c)
		}
	}
	for _, ep := range []string{"", "https://s3.example.com", "http://127.0.0.1:9000", "http://localhost:9000", "http://[::1]:9000"} {
		s, err := filestore.Open(ctx, filestore.Config{Dir: dir, Bucket: "s3://files/team/", Endpoint: ep, AccessKeyID: "k", SecretKey: "s"})
		if err != nil {
			t.Errorf("endpoint %q: %v", ep, err)
			continue
		}
		if s.Name() != "s3" || !strings.Contains(s.Describe(), "s3://files/team/") {
			t.Errorf("endpoint %q: %s %s", ep, s.Name(), s.Describe())
		}
	}
}

func TestLocalIsTheDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "files")
	s, err := filestore.Open(context.Background(), filestore.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "local" || s.Describe() != "folder "+dir {
		t.Fatalf("%s %s", s.Name(), s.Describe())
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	conformance(t, s)
}

func TestFromEnv(t *testing.T) {
	t.Setenv("OPENNEKO_FILES_BUCKET", " s3://files ")
	t.Setenv("OPENNEKO_FILES_BUCKET_ENDPOINT", "https://s3.example.com")
	t.Setenv("OPENNEKO_FILES_BUCKET_REGION", "eu-west-1")
	t.Setenv("OPENNEKO_FILES_BUCKET_KEY_ID", "k")
	t.Setenv("OPENNEKO_FILES_BUCKET_SECRET", "s")
	got := filestore.FromEnv("/data/files")
	want := filestore.Config{Dir: "/data/files", Bucket: "s3://files", Endpoint: "https://s3.example.com", Region: "eu-west-1", AccessKeyID: "k", SecretKey: "s"}
	if got != want {
		t.Fatalf("%+v", got)
	}
}

func TestLocalFilesLeft(t *testing.T) {
	dir := t.TempDir()
	if n := filestore.LocalFilesLeft(dir, 10); n != 0 {
		t.Fatalf("empty folder: %d", n)
	}
	for i := range 3 {
		p := filepath.Join(dir, "orgs", "o", "users", "u", fmt.Sprintf("f%d.txt", i))
		os.MkdirAll(filepath.Dir(p), 0o755) //nolint:errcheck
		os.WriteFile(p, []byte("x"), 0o644) //nolint:errcheck
	}
	os.WriteFile(filepath.Join(dir, ".openneko-upload-1"), []byte("x"), 0o644) //nolint:errcheck
	if n := filestore.LocalFilesLeft(dir, 10); n != 3 {
		t.Fatalf("got %d, want 3", n)
	}
	if n := filestore.LocalFilesLeft(dir, 2); n != 2 {
		t.Fatalf("limit: got %d", n)
	}
}

func TestBucket(t *testing.T) {
	endpoint := s3test.Start(t)
	name := s3test.Bucket(t, endpoint, "openneko-files")
	ctx := context.Background()
	s, err := filestore.Open(ctx, filestore.Config{Dir: t.TempDir(), Bucket: "s3://" + name + "/install-a", Endpoint: endpoint, AccessKeyID: s3test.KeyID, SecretKey: s3test.Secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	conformance(t, s)

	other, err := filestore.Open(ctx, filestore.Config{Dir: t.TempDir(), Bucket: "s3://" + name + "/install-b", Endpoint: endpoint, AccessKeyID: s3test.KeyID, SecretKey: s3test.Secret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "orgs/o/users/u/a.txt", strings.NewReader("a"), fstable.PutMeta{}); err != nil {
		t.Fatal(err)
	}
	if es, _, err := other.List(ctx, fstable.ListOpts{Prefix: "orgs/"}); err != nil || len(es) != 0 {
		t.Fatalf("a second prefix sees the first one's files: %v %v", es, err)
	}

	wrong, _ := filestore.Open(ctx, filestore.Config{Dir: t.TempDir(), Bucket: "s3://" + name, Endpoint: endpoint, AccessKeyID: s3test.KeyID, SecretKey: "not-the-secret"})
	err = wrong.Check(ctx)
	if err == nil {
		t.Fatal("a wrong secret passed the check")
	}
	if strings.Contains(err.Error(), "not-the-secret") {
		t.Fatalf("the check error holds the secret: %v", err)
	}
	missing, _ := filestore.Open(ctx, filestore.Config{Dir: t.TempDir(), Bucket: "s3://no-such-bucket", Endpoint: endpoint, AccessKeyID: s3test.KeyID, SecretKey: s3test.Secret})
	if err := missing.Check(ctx); err == nil {
		t.Fatal("a missing bucket passed the check")
	}
}

type failing struct{ n int }

func (f *failing) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("client went away")
	}
	k := min(len(p), f.n)
	for i := range k {
		p[i] = 'z'
	}
	f.n -= k
	return k, nil
}

func conformance(t *testing.T, s filestore.Store) {
	t.Helper()
	ctx := context.Background()
	const scope = "orgs/o1/users/u1/"
	names := []string{"a.txt", "b/c.csv", "b/d.md", "e.pdf", "f.txt"}
	for _, n := range names {
		e, err := s.Put(ctx, scope+n, strings.NewReader("body of "+n), fstable.PutMeta{})
		if err != nil {
			t.Fatalf("put %s: %v", n, err)
		}
		if e.Key != scope+n || e.Size != int64(len("body of "+n)) || e.ETag == "" {
			t.Fatalf("put %s gave %+v", n, e)
		}
	}
	if _, err := s.Put(ctx, "orgs/o1/users/u2/x.txt", strings.NewReader("other user"), fstable.PutMeta{}); err != nil {
		t.Fatal(err)
	}

	var keys []string
	after := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("list does not end")
		}
		es, next, err := s.List(ctx, fstable.ListOpts{Prefix: scope, Limit: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		if len(es) > 2 {
			t.Fatalf("page of %d", len(es))
		}
		for _, e := range es {
			keys = append(keys, e.Key)
		}
		if next == "" {
			break
		}
		after = next
	}
	if strings.Join(keys, ",") != scope+strings.Join(names, ","+scope) {
		t.Fatalf("list = %v", keys)
	}
	if es, _, err := s.List(ctx, fstable.ListOpts{Prefix: "orgs/nobody/"}); err != nil || len(es) != 0 {
		t.Fatalf("empty prefix: %v %v", es, err)
	}

	st, err := s.Stat(ctx, scope+"b/c.csv")
	if err != nil || st.Size != int64(len("body of b/c.csv")) || st.ModifiedAt.IsZero() {
		t.Fatalf("stat: %+v %v", st, err)
	}
	body, _, err := s.Get(ctx, scope+"b/c.csv")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "body of b/c.csv" {
		t.Fatalf("get = %q", data)
	}

	if _, err := s.Put(ctx, scope+"a.txt", &failing{n: 70000}, fstable.PutMeta{}); err == nil {
		t.Fatal("a body that fails part way was stored")
	}
	body, _, err = s.Get(ctx, scope+"a.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(body)
	body.Close()
	if string(data) != "body of a.txt" {
		t.Fatalf("a failed write replaced the file: %q", data)
	}

	if _, err := s.Stat(ctx, scope+"missing.txt"); !errors.Is(err, fstable.ErrNotFound) {
		t.Fatalf("stat missing: %v", err)
	}
	if _, _, err := s.Get(ctx, scope+"missing.txt"); !errors.Is(err, fstable.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if err := s.Delete(ctx, scope+"a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, scope+"a.txt"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.Stat(ctx, scope+"a.txt"); !errors.Is(err, fstable.ErrNotFound) {
		t.Fatalf("stat deleted: %v", err)
	}
	if s.Name() == "local" {
		return
	}
	for _, bad := range []string{"", "/x", "../x", "orgs/../../x", "a//b", "a\\b"} {
		if _, err := s.Put(ctx, bad, strings.NewReader("x"), fstable.PutMeta{}); err == nil {
			t.Errorf("put %q was accepted", bad)
		}
	}
}
