package files

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dosco/graphjin/core/v3/fstable"
)

// noSendfile is a ResponseWriter whose ReadFrom fails as sendfile does in the
// macOS confined network part: the body stops after net/http's 512-byte sniff.
type noSendfile struct {
	*httptest.ResponseRecorder
	readFrom int
}

func (w *noSendfile) ReadFrom(io.Reader) (int64, error) {
	w.readFrom++
	return 0, errors.New("sendfile: operation not permitted")
}

func TestContentNeverUsesTheWritersReadFrom(t *testing.T) {
	root := t.TempDir()
	store, err := fstable.NewLocal(fstable.LocalConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 64<<10)
	_, _ = rand.Read(want)
	p := filepath.Join(root, "orgs", "o1", "users", "u1", "library", "alternates.xlsx")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, want, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(store, nil, testKey, Options{})
	req := httptest.NewRequest(http.MethodGet, "/v1/files/content?path=library/alternates.xlsx", nil)
	req.Header.Set("Authorization", "Bearer "+mint(testKey, "o1", "u1", time.Now().Add(time.Minute)))
	w := &noSendfile{ResponseRecorder: httptest.NewRecorder()}
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.readFrom != 0 || !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("status %d, ReadFrom called %d times, %d of %d bytes", w.Code, w.readFrom, w.Body.Len(), len(want))
	}
}
