package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestDockerReaperOnlyRemovesExitedOpenNekoSandboxesAndOldUnreferencedDigests(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	t.Setenv("OPENNEKO_VERSION", "v3")
	t.Setenv("OPENNEKO_AGENT_IMAGE", "")
	var mu sync.Mutex
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/containers/running/update" {
			var body struct {
				RestartPolicy struct {
					Name string `json:"Name"`
				} `json:"RestartPolicy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RestartPolicy.Name != "no" {
				t.Errorf("unexpected restart update: %v, %+v", err, body)
			}
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/containers/json":
			_ = json.NewEncoder(w).Encode([]dockerContainerSummary{
				{ID: "old", Names: []string{"/openshell-warm-old"}, Image: "ghcr.io/open-neko/agent:v2", ImageID: "sha256:agent", State: "exited"},
				{ID: "new", Names: []string{"/openshell-default--openneko-warm-new"}, Image: "sha256:new-agent", ImageID: "sha256:new-agent", State: "exited"},
				{ID: "recent", Names: []string{"/openshell-work-recent"}, Image: "ghcr.io/open-neko/agent:v3", ImageID: "sha256:agent", State: "exited"},
				{ID: "foreign", Names: []string{"/openshell-warm-foreign"}, Image: "other/agent:v1", ImageID: "sha256:foreign", State: "exited"},
				{ID: "running", Names: []string{"/openshell-warm-running"}, Image: "ghcr.io/open-neko/agent:v3", ImageID: "sha256:agent", State: "running"},
				{ID: "librarian", Names: []string{"/openneko-librarian"}, Image: librarianRepo + ":v3", ImageID: "sha256:current", State: "running"},
			})
		case "/containers/old/json", "/containers/recent/json":
			finished := now.Add(-10 * time.Minute)
			if r.URL.Path == "/containers/recent/json" {
				finished = now.Add(-10 * time.Second)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name":   "/openshell-warm-old",
				"Config": map[string]any{"Image": "ghcr.io/open-neko/agent:v2", "Labels": map[string]string{managedByOpenShell: "openshell"}},
				"State":  map[string]any{"Status": "exited", "FinishedAt": finished},
			})
		case "/containers/new/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name":   "/openshell-default--openneko-warm-new",
				"Config": map[string]any{"Image": "sha256:new-agent", "Labels": map[string]string{managedByOpenShell: "openshell", "openshell.ai/sandbox-name": "openneko-warm-new"}},
				"State":  map[string]any{"Status": "exited", "FinishedAt": now.Add(-time.Hour)},
			})
		case "/containers/running/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name":       "/openshell-warm-running",
				"Config":     map[string]any{"Image": "ghcr.io/open-neko/agent:v3", "Labels": map[string]string{managedByOpenShell: "openshell"}},
				"State":      map[string]any{"Status": "running"},
				"HostConfig": map[string]any{"RestartPolicy": map[string]string{"Name": "always"}},
			})
		case "/images/json":
			_ = json.NewEncoder(w).Encode([]dockerImageSummary{
				{ID: "sha256:old-image", RepoTags: []string{librarianRepo + "@sha256:old"}, RepoDigests: []string{librarianRepo + "@sha256:old"}, Created: now.Add(-48 * time.Hour).Unix()},
				{ID: "sha256:old-agent", RepoTags: []string{"ghcr.io/open-neko/agent:v2"}, Created: now.Add(-48 * time.Hour).Unix()},
				{ID: "sha256:current-agent", RepoTags: []string{"ghcr.io/open-neko/agent:v3"}, Created: now.Add(-48 * time.Hour).Unix()},
				{ID: "sha256:recent-image", RepoDigests: []string{librarianRepo + "@sha256:recent"}, Created: now.Add(-time.Hour).Unix()},
				{ID: "sha256:current", RepoTags: []string{librarianRepo + ":v3"}, RepoDigests: []string{librarianRepo + "@sha256:current"}, Created: now.Add(-48 * time.Hour).Unix()},
				{ID: "sha256:foreign-image", RepoDigests: []string{"foreign/librarian@sha256:old"}, Created: now.Add(-48 * time.Hour).Unix()},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	reaper := &dockerReaper{client: server.Client(), base: server.URL}
	containers, images, err := reaper.sweep(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if containers != 2 || images != 2 {
		t.Fatalf("removed containers=%d images=%d, want 2 and 2", containers, images)
	}
	mu.Lock()
	sort.Strings(deleted)
	got := append([]string(nil), deleted...)
	mu.Unlock()
	want := []string{"/containers/new", "/containers/old", "/containers/running/update", "/images/ghcr.io/open-neko/agent:v2", "/images/ghcr.io/open-neko/neko-librarian@sha256:old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted %v, want %v", got, want)
	}
}

func TestDockerReaperKeepsExitedContainerWithoutOwnershipLabel(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			_ = json.NewEncoder(w).Encode([]dockerContainerSummary{{ID: "old", Names: []string{"/openshell-warm-old"}, Image: "ghcr.io/open-neko/agent:v2", State: "exited"}})
		case "/containers/old/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name":   "/openshell-warm-old",
				"Config": map[string]any{"Image": "ghcr.io/open-neko/agent:v2", "Labels": map[string]string{}},
				"State":  map[string]any{"Status": "exited", "FinishedAt": now.Add(-time.Hour)},
			})
		case "/images/json":
			_ = json.NewEncoder(w).Encode([]dockerImageSummary{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	reaper := &dockerReaper{client: server.Client(), base: server.URL}
	containers, images, err := reaper.sweep(context.Background(), now)
	if err != nil || containers != 0 || images != 0 {
		t.Fatalf("sweep = %d, %d, %v; want no removals", containers, images, err)
	}
}
