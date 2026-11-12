package webfront

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type remoteWorker struct {
	mu      sync.Mutex
	calls   []packRequest
	updates func() (int, string)
	upload  func() (int, string)
}

func (rw *remoteWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/auth/status" {
		_, _ = w.Write([]byte(liveScalekit))
		return
	}
	body, _ := io.ReadAll(r.Body)
	rw.mu.Lock()
	rw.calls = append(rw.calls, packRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("X-OpenNeko-Actor"), string(body)})
	reply := rw.updates
	if r.URL.Path == "/admin/packs/upload" {
		reply = rw.upload
	}
	rw.mu.Unlock()
	status, text := reply()
	w.WriteHeader(status)
	_, _ = w.Write([]byte(text))
}

func (rw *remoteWorker) take() []packRequest {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	calls := rw.calls
	rw.calls = nil
	return calls
}

func remoteFront(t *testing.T) (*Front, *remoteWorker) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENNEKO_BUILTIN_SKILLS_ROOT", t.TempDir())
	db := testpg.Migrated(t)
	seedOrg(t, db)
	wk := &remoteWorker{
		updates: func() (int, string) { return 200, `{"updates":{"tip":null,"skills":[],"packs":[]}}` },
		upload:  func() (int, string) { return 200, `{"ok":true}` },
	}
	srv := httptest.NewServer(wk)
	t.Cleanup(srv.Close)
	g := newGate(t, srv.URL)
	g.DB = db
	return &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API:  &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "https://neko.example", SecretsDir: t.TempDir()},
		Node: &recorder{},
	}, wk
}

func remoteAudit(t *testing.T, f *Front) string {
	t.Helper()
	rows, err := f.API.DB.Query(context.Background(), `select event from audit_chain where org_id = 'org' and entity_kind = 'context_remote' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		events = append(events, e)
	}
	return strings.Join(events, ",")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestContextRemoteAdminOnly(t *testing.T) {
	f, wk := remoteFront(t)
	member := sessionCookie("member")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/context-remote", ""},
		{"PUT", "/api/v1/admin/context-remote", `{"url":"https://github.com/a/b.git","mode":"push","branch":"main","kinds":["skills"]}`},
		{"DELETE", "/api/v1/admin/context-remote", ""},
		{"POST", "/api/v1/admin/context-remote/host-key", ""},
		{"GET", "/api/v1/admin/context-remote/publish", ""},
		{"POST", "/api/v1/admin/context-remote/publish", ""},
		{"GET", "/api/v1/admin/context-remote/updates", ""},
		{"POST", "/api/v1/admin/context-remote/updates", `{"skills":[],"packs":[]}`},
	} {
		if w := call(f, c.method, c.path, c.body, member); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"admin only"}` {
			t.Errorf("%s %s as a member: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
		if w := call(f, c.method, c.path, c.body, nil); w.Code == 200 {
			t.Errorf("%s %s as anonymous: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	var n int
	_ = f.API.DB.QueryRow(context.Background(), `select count(*) from context_remote`).Scan(&n)
	if n != 0 || len(wk.take()) != 0 {
		t.Errorf("rows %d, worker calls made", n)
	}
}

func TestContextRemoteSettings(t *testing.T) {
	f, _ := remoteFront(t)
	admin := sessionCookie("admin")
	const path = "/api/v1/admin/context-remote"
	if w := call(f, "GET", path, "", admin); w.Code != 200 || w.Body.String() != `{"remote":null}` {
		t.Fatalf("empty: %d %s", w.Code, w.Body.String())
	}
	w := call(f, "PUT", path, `{"url":"  https://GitLab.com:443/acme/context.git ","mode":"push","branch":" main ","kinds":["skills","workflows","skills"],"username":" bot ","token":" glpat-secret "}`, admin)
	want := `{"remote":{"url":"https://gitlab.com/acme/context.git","transport":"https","ssh":null,"provider":"gitlab","mode":"push","branch":"main","kinds":["skills","workflows"],"username":"bot","hasToken":true,"lastPublish":null}}`
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var stored, updatedBy string
	_ = f.API.DB.QueryRow(context.Background(), `select token, updated_by_user_id from context_remote where org_id = 'org'`).Scan(&stored, &updatedBy)
	if plain, err := config.MaybeDecryptValue(f.API.SecretsDir, stored); err != nil || plain != "glpat-secret" || stored == plain || updatedBy != "admin" {
		t.Errorf("stored token %q -> %q %v, by %q", stored, plain, err, updatedBy)
	}
	if w := call(f, "PUT", path, `{"url":"https://gitlab.com/acme/context.git","mode":"pull_request","branch":"release","kinds":["memory"]}`, admin); !strings.Contains(w.Body.String(), `"hasToken":true`) {
		t.Errorf("token kept: %s", w.Body.String())
	}
	if w := call(f, "PUT", path, `{"url":"https://gitlab.com/acme/context.git","mode":"push","branch":"main","kinds":["skills"],"token":null}`, admin); !strings.Contains(w.Body.String(), `"hasToken":false`) || !strings.Contains(w.Body.String(), `"username":null`) {
		t.Errorf("token removed: %s", w.Body.String())
	}
	for _, c := range []struct{ body, message string }{
		{`{"url":"not a url","mode":"push","branch":"main","kinds":["skills"]}`, "Enter the repository's address, such as https://github.com/acme/openneko-context.git or git@github.com:acme/openneko-context.git."},
		{`{"url":"http://gitlab.com/a/b.git","mode":"push","branch":"main","kinds":["skills"]}`, "The repository address must start with https:// or ssh://, or have the form git@host:org/repo.git."},
		{`{"url":"https://u:p@github.com/a/b.git","mode":"push","branch":"main","kinds":["skills"]}`, "Put the access token in the token field, not in the address."},
		{`{"url":"https://github.com/a","mode":"push","branch":"main","kinds":["skills"]}`, "The address must name a repository, such as https://github.com/acme/openneko-context.git."},
		{`{"url":"https://github.com/a/b.git?x=1","mode":"push","branch":"main","kinds":["skills"]}`, "The address must name a repository, such as https://github.com/acme/openneko-context.git."},
		{`{"url":"https://github.com/a/b.git","mode":"merge","branch":"main","kinds":["skills"]}`, "Choose push or pull request."},
		{`{"url":"https://github.com/a/b.git","mode":"push","branch":"a..b","kinds":["skills"]}`, "Enter a valid branch name, such as main."},
		{`{"url":"https://github.com/a/b.git","mode":"push","branch":"x.lock","kinds":["skills"]}`, "Enter a valid branch name, such as main."},
		{`{"url":"https://github.com/a/b.git","mode":"push","branch":"main","kinds":[]}`, "Choose at least one kind of context to publish."},
		{`{"url":"https://github.com/a/b.git","mode":"push","branch":"main","kinds":["library"]}`, "Choose at least one kind of context to publish."},
		{`{"url":"git@nohost.invalid:acme/context.git","mode":"push","branch":"main","kinds":["skills"]}`, ""},
		{`{"url":"ssh://git@127.0.0.1:1/acme/context.git","mode":"push","branch":"main","kinds":["skills"]}`, "OpenNeko could not read the SSH host key of 127.0.0.1 on port 1."},
	} {
		w := call(f, "PUT", path, c.body, admin)
		if c.message == "" {
			if w.Code != 400 || !strings.Contains(w.Body.String(), "could not read the SSH host key of nohost.invalid on port 22") {
				t.Errorf("short SSH form: %d %s", w.Code, w.Body.String())
			}
			continue
		}
		if w.Code != 400 || decode(t, w)["error"] != c.message {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{"", "[1]", "null"} {
		if w := call(f, "PUT", path, body, admin); w.Code != 400 || w.Body.String() != `{"error":"A JSON body is required."}` {
			t.Errorf("body %q: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := call(f, "POST", path+"/host-key", "", admin); w.Code != 400 || w.Body.String() != `{"error":"This remote has no SSH host key to confirm."}` {
		t.Errorf("host key on HTTPS: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", path, "", admin); w.Code != 200 || w.Body.String() != `{"remote":null}` {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	for _, p := range []string{"/publish", "/updates"} {
		for _, m := range []string{"GET", "POST"} {
			if w := call(f, m, path+p, "", admin); w.Code != 400 || !strings.HasPrefix(w.Body.String(), `{"error":"Connect a remote repository first."`) {
				t.Errorf("%s %s with no remote: %d %s", m, p, w.Code, w.Body.String())
			}
		}
	}
	if got := remoteAudit(t, f); got != "context:remote_changed,context:remote_changed,context:remote_changed,context:remote_removed" {
		t.Errorf("audit %s", got)
	}
}

func sshTestServer(t *testing.T) (port string, hostKey ssh.PublicKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _, _, _ = ssh.NewServerConn(conn, config)
				conn.Close()
			}()
		}
	}()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port, signer.PublicKey()
}

func TestContextRemoteSSH(t *testing.T) {
	f, _ := remoteFront(t)
	admin := sessionCookie("admin")
	port, hostKey := sshTestServer(t)
	w := call(f, "PUT", "/api/v1/admin/context-remote", `{"url":"ssh://git@127.0.0.1:`+port+`/acme/context.git","mode":"push","branch":"main","kinds":["skills"]}`, admin)
	var got struct {
		Remote contextRemoteSettings `json:"remote"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	s := got.Remote.SSH
	if got.Remote.Transport != "ssh" || s == nil || s.HostConfirmed ||
		len(s.HostFingerprints) != 1 || s.HostFingerprints[0] != "ED25519 "+ssh.FingerprintSHA256(hostKey) ||
		!strings.HasPrefix(s.PublicKey, "ssh-ed25519 ") || !strings.HasSuffix(s.PublicKey, " openneko-org") {
		t.Fatalf("ssh settings %+v", got.Remote)
	}
	var enc, hosts string
	_ = f.API.DB.QueryRow(context.Background(), `select ssh_private_key, ssh_known_hosts from context_remote`).Scan(&enc, &hosts)
	plain, _ := config.MaybeDecryptValue(f.API.SecretsDir, enc)
	signer, err := ssh.ParsePrivateKey([]byte(plain))
	if err != nil || enc == plain || !strings.HasPrefix(s.PublicKey, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))) {
		t.Errorf("deploy key: %v", err)
	}
	if !strings.HasPrefix(hosts, "[127.0.0.1]:"+port+" ssh-ed25519 ") {
		t.Errorf("known hosts %q", hosts)
	}

	w = call(f, "PUT", "/api/v1/admin/context-remote", `{"url":"ssh://git@127.0.0.1:`+port+`/acme/other.git","mode":"push","branch":"main","kinds":["skills"]}`, admin)
	if !strings.Contains(w.Body.String(), s.PublicKey) {
		t.Errorf("the key changed on a plain save: %s", w.Body.String())
	}
	w = call(f, "PUT", "/api/v1/admin/context-remote", `{"url":"ssh://git@127.0.0.1:`+port+`/acme/other.git","mode":"push","branch":"main","kinds":["skills"],"newSshKey":true}`, admin)
	if strings.Contains(w.Body.String(), s.PublicKey) {
		t.Errorf("newSshKey kept the key: %s", w.Body.String())
	}

	w = call(f, "POST", "/api/v1/admin/context-remote/publish", "", admin)
	if w.Code != 400 || decode(t, w)["error"] != "Confirm the server's SSH host key first." ||
		!strings.Contains(w.Body.String(), `"lastPublish":{"at":"`) || !strings.Contains(w.Body.String(), `"status":"failed","detail":"Confirm the server's SSH host key first.","link":null}`) {
		t.Errorf("publish before confirm: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/context-remote/publish", "", admin); w.Code != 400 || decode(t, w)["error"] != "Confirm the server's SSH host key first." {
		t.Errorf("preview before confirm: %d %s", w.Code, w.Body.String())
	}
	w = call(f, "POST", "/api/v1/admin/context-remote/host-key", "", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"hostConfirmed":true`) {
		t.Errorf("confirm: %d %s", w.Code, w.Body.String())
	}
	if got := remoteAudit(t, f); got != "context:remote_changed,context:remote_changed,context:remote_changed,context:host_key_confirmed" {
		t.Errorf("audit %s", got)
	}
}

func TestHostKeyFingerprints(t *testing.T) {
	line := "git.acme.local ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n# note\nbroken line\n"
	got := hostKeyFingerprints(line)
	if len(got) != 1 || !strings.HasPrefix(got[0], "ED25519 SHA256:") {
		t.Fatalf("fingerprints %v", got)
	}
	if _, err := exec.LookPath("ssh-keygen"); err == nil {
		file := filepath.Join(t.TempDir(), "known_hosts")
		_ = os.WriteFile(file, []byte(line), 0o600)
		out, _ := exec.Command("ssh-keygen", "-l", "-E", "sha256", "-f", file).Output()
		if fields := strings.Fields(string(out)); len(fields) < 2 || got[0] != "ED25519 "+fields[1] {
			t.Errorf("ssh-keygen says %q, Go says %q", out, got[0])
		}
	}
	if _, err := exec.LookPath("ssh-keyscan"); err == nil {
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:22", time.Second); err == nil {
			conn.Close()
			keyscan, _ := exec.Command("ssh-keyscan", "-T", "5", "127.0.0.1").Output()
			file := filepath.Join(t.TempDir(), "scanned")
			_ = os.WriteFile(file, keyscan, 0o600)
			listed, _ := exec.Command("ssh-keygen", "-l", "-E", "sha256", "-f", file).Output()
			var want []string
			for _, l := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
				if f := strings.Fields(l); len(f) == 4 {
					want = append(want, strings.Trim(f[3], "()")+" "+f[1])
				}
			}
			scanned, err := scanHostKeys("127.0.0.1", 22)
			got := hostKeyFingerprints(scanned)
			slices.Sort(want)
			slices.Sort(got)
			if err != nil || len(want) == 0 || !slices.Equal(got, want) {
				t.Errorf("local sshd: Go %v, OpenSSH %v (%v)", got, want, err)
			}
		}
	}
	if got := hostKeyFingerprints(""); len(got) != 0 {
		t.Errorf("empty %v", got)
	}
}

// remoteRepo makes an org config repo with a skill, a workflow and a
// memory, and a bare remote that the row points at.
func remoteRepo(t *testing.T, f *Front, mode string) (root, bare string) {
	t.Helper()
	allowFileRemotes = true
	t.Cleanup(func() { allowFileRemotes = false })
	root = orgAgentRoot("org")
	for path, text := range map[string]string{
		"skills/shortfall/SKILL.md":        "# Shortfall\n",
		"skills/shortfall/.deployed":       "local\n",
		"workflows/daily.md":               "# Daily\n",
		"memory/business_rule/m.md":        "rule\n",
		"skill-overlays/shortfall/note.md": "note\n",
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755)
		_ = os.WriteFile(filepath.Join(root, path), []byte(text), 0o644)
	}
	if _, err := commitConfigRepo(root, ".", "seed"); err != nil {
		t.Fatal(err)
	}
	bare = filepath.Join(t.TempDir(), "remote.git")
	runGit(t, root, "init", "-q", "--bare", "--initial-branch=main", bare)
	_, err := f.API.DB.Exec(context.Background(), `insert into context_remote (org_id, url, mode, branch, kinds)
		values ('org', $1, $2, 'main', '["skills","workflows"]')`, "file://"+bare, mode)
	if err != nil {
		t.Fatal(err)
	}
	return root, bare
}

func remoteChange(t *testing.T, bare string, files map[string]string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(clone), "clone", "-q", bare, clone)
	for path, text := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(clone, path)), 0o755)
		_ = os.WriteFile(filepath.Join(clone, path), []byte(text), 0o644)
	}
	runGit(t, clone, "add", "-A")
	runGit(t, clone, "commit", "-q", "-m", "Remote change")
	runGit(t, clone, "push", "-q", "origin", "HEAD:main")
	return runGit(t, bare, "rev-parse", "main")
}

func TestContextRemotePublish(t *testing.T) {
	f, _ := remoteFront(t)
	admin := sessionCookie("admin")
	root, bare := remoteRepo(t, f, "push")
	const path = "/api/v1/admin/context-remote/publish"

	w := call(f, "GET", path, "", admin)
	if w.Code != 200 || w.Body.String() != `{"items":[{"kind":"skills","name":"shortfall","added":1,"changed":0,"removed":0},{"kind":"workflows","name":"daily","added":1,"changed":0,"removed":0}]}` {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", path+"?kind=workflows&name=daily", "", admin); w.Code != 200 || !strings.Contains(decode(t, w)["diff"].(string), "+# Daily") {
		t.Errorf("item diff: %d %s", w.Code, w.Body.String())
	}
	for q, msg := range map[string]string{"?kind=memory&name=m": "This remote does not publish that kind.", "?kind=skills&name=..": "Unknown item."} {
		if w := call(f, "GET", path+q, "", admin); w.Code != 400 || decode(t, w)["error"] != msg {
			t.Errorf("%s: %d %s", q, w.Code, w.Body.String())
		}
	}

	w = call(f, "POST", path, "", admin)
	if w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Result publishResult         `json:"result"`
		Remote contextRemoteSettings `json:"remote"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	head := runGit(t, root, "rev-parse", "HEAD")
	if out.Result.Status != "pushed" || out.Result.Detail != "Created main on the remote." || out.Result.Head != head ||
		out.Result.SHA == nil || *out.Result.SHA != runGit(t, bare, "rev-parse", "main") || out.Result.Link != nil ||
		out.Remote.LastPublish == nil || out.Remote.LastPublish.Status != "ok" {
		t.Fatalf("publish result %s", w.Body.String())
	}
	if files := runGit(t, bare, "ls-tree", "-r", "--name-only", "main"); files != "skills/shortfall/SKILL.md\nworkflows/daily.md" {
		t.Errorf("remote files %q", files)
	}
	var bases string
	_ = f.API.DB.QueryRow(context.Background(), `select skill_bases::text from context_remote`).Scan(&bases)
	if !strings.Contains(bases, `"shortfall": {"local"`) {
		t.Errorf("skill bases %s", bases)
	}
	if w := call(f, "POST", path, "", admin); !strings.Contains(w.Body.String(), `"status":"unchanged"`) {
		t.Errorf("second publish: %s", w.Body.String())
	}

	tip := remoteChange(t, bare, map[string]string{"README.md": "readme\n"})
	_ = os.WriteFile(filepath.Join(root, "workflows/daily.md"), []byte("# Daily v2\n"), 0o644)
	_, _ = commitConfigRepo(root, "workflows", "edit")
	if w := call(f, "GET", path, "", admin); w.Body.String() != `{"items":[{"kind":"workflows","name":"daily","added":0,"changed":1,"removed":0}]}` {
		t.Errorf("preview after edit: %s", w.Body.String())
	}
	w = call(f, "POST", path, "", admin)
	if !strings.Contains(w.Body.String(), `"detail":"Pushed to main."`) || runGit(t, bare, "rev-parse", "main^") != tip ||
		runGit(t, bare, "show", "main:README.md") != "readme" || runGit(t, bare, "show", "main:workflows/daily.md") != "# Daily v2" {
		t.Errorf("publish onto the remote tip: %s", w.Body.String())
	}

	_, _ = f.API.DB.Exec(context.Background(), `update context_remote set mode = 'pull_request'`)
	_ = os.WriteFile(filepath.Join(root, "workflows/daily.md"), []byte("# Daily v3\n"), 0o644)
	_, _ = commitConfigRepo(root, "workflows", "edit")
	w = call(f, "POST", path, "", admin)
	out.Result = publishResult{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Result.Status != "pull_request" || !strings.HasPrefix(out.Result.Branch, "openneko/context-") || out.Result.Link != nil ||
		out.Result.Head != "" || !strings.Contains(w.Body.String(), `"detail":"Pushed openneko/context-`) {
		t.Errorf("pull request mode: %s", w.Body.String())
	}
	if got := runGit(t, bare, "show", "main:workflows/daily.md"); got != "# Daily v2" {
		t.Errorf("main changed in pull request mode: %q", got)
	}
	if got := remoteAudit(t, f); got != "context:published,context:published,context:published,context:published" {
		t.Errorf("audit %s", got)
	}
}

func TestContextRemoteEmptyRepo(t *testing.T) {
	f, _ := remoteFront(t)
	admin := sessionCookie("admin")
	allowFileRemotes = true
	t.Cleanup(func() { allowFileRemotes = false })
	_, _ = f.API.DB.Exec(context.Background(), `insert into context_remote (org_id, url, mode, branch) values ('org', 'file:///nowhere/x.git', 'push', 'main')`)
	if w := call(f, "GET", "/api/v1/admin/context-remote/publish", "", admin); w.Code != 200 || w.Body.String() != `{"items":[]}` {
		t.Errorf("preview: %d %s", w.Code, w.Body.String())
	}
	w := call(f, "POST", "/api/v1/admin/context-remote/publish", "", admin)
	if w.Code != 400 || decode(t, w)["error"] != "There is nothing to publish yet." || !strings.Contains(w.Body.String(), `"status":"failed"`) {
		t.Errorf("publish: %d %s", w.Code, w.Body.String())
	}
}

func TestContextRemoteUpdates(t *testing.T) {
	f, wk := remoteFront(t)
	admin := sessionCookie("admin")
	root, bare := remoteRepo(t, f, "push")
	const path = "/api/v1/admin/context-remote/updates"

	if w := call(f, "GET", path+"?skill=shortfall", "", admin); w.Code != 400 || decode(t, w)["error"] != "Check for updates first." {
		t.Errorf("diff before a check: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", path, `{"skills":[{"name":"shortfall","choice":"remote"}]}`, admin); w.Code != 400 || decode(t, w)["error"] != "Check for updates first." {
		t.Errorf("apply before a check: %d %s", w.Code, w.Body.String())
	}
	call(f, "POST", "/api/v1/admin/context-remote/publish", "", admin)
	remoteChange(t, bare, map[string]string{
		"skills/shortfall/SKILL.md": "# Shortfall from upstream\n",
		"packs/demo-pack/pack.yaml": "id: demo-pack\n",
	})
	row, _ := f.API.readContextRemote(context.Background(), "org")
	access, _ := f.API.remoteAccess(row)
	if _, err := fetchRemote(root, access, "main"); err != nil {
		t.Fatal(err)
	}

	wk.updates = func() (int, string) {
		return 200, `{"updates":{"tip":"abc","skills":[{"name":"shortfall","status":"update","remoteTree":"t"}],"packs":[]}}`
	}
	if w := call(f, "GET", path, "", admin); w.Code != 200 || w.Body.String() != `{"updates":{"tip":"abc","skills":[{"name":"shortfall","status":"update","remoteTree":"t"}],"packs":[]}}` {
		t.Errorf("check: %d %s", w.Code, w.Body.String())
	}
	if calls := wk.take(); len(calls) != 1 || calls[0].method != "GET" || calls[0].uri != "/admin/context-remote/updates" {
		t.Errorf("worker calls %v", calls)
	}
	if w := call(f, "GET", path+"?skill=shortfall", "", admin); w.Code != 200 || !strings.Contains(decode(t, w)["diff"].(string), "+# Shortfall from upstream") || strings.Contains(w.Body.String(), ".deployed") {
		t.Errorf("incoming diff: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", path, `{"skills":[{"name":"nope","choice":"remote"}]}`, admin); w.Code != 400 || decode(t, w)["error"] != "The repository has no skill named nope." {
		t.Errorf("unknown skill: %d %s", w.Code, w.Body.String())
	}

	wk.upload = func() (int, string) { return 400, `{"error":"bad pack"}` }
	w := call(f, "POST", path, `{"skills":[{"name":"shortfall","choice":"remote"},{"name":1,"choice":"remote"},{"name":"x","choice":"maybe"}],"packs":["demo-pack",3]}`, admin)
	if w.Code != 200 {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	head := runGit(t, root, "rev-parse", "HEAD")
	if want := `{"sha":"` + head + `","packs":[{"id":"demo-pack","ok":false,"error":"bad pack"}],"updates":{"tip":"abc","skills":[{"name":"shortfall","status":"update","remoteTree":"t"}],"packs":[]}}`; w.Body.String() != want {
		t.Errorf("apply reply %s", w.Body.String())
	}
	if got := runGit(t, root, "show", "HEAD:skills/shortfall/SKILL.md"); got != "# Shortfall from upstream" {
		t.Errorf("skill after bring-in %q", got)
	}
	if got := runGit(t, root, "log", "-1", "--pretty=%s"); !strings.HasPrefix(got, "Brought in skill from ") || !strings.HasSuffix(got, "remote: shortfall") {
		t.Errorf("commit message %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "skills/shortfall/.deployed")); err != nil {
		t.Errorf("local-only file removed: %v", err)
	}
	calls := wk.take()
	if len(calls) != 2 || calls[0].uri != "/admin/packs/upload" || calls[0].contentType != "application/zip" || calls[0].actor != "admin" || calls[1].uri != "/admin/context-remote/updates" {
		t.Fatalf("worker calls %v", calls)
	}
	zr, err := zip.NewReader(bytes.NewReader([]byte(calls[0].body)), int64(len(calls[0].body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range zr.File {
		names = append(names, file.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "demo-pack/pack.yaml") {
		t.Errorf("archive %v", names)
	}
	var changes int
	_ = f.API.DB.QueryRow(context.Background(), `select count(*) from config_change where org_id = 'org' and artifact_kind = 'skill' and commit_sha = $1`, head).Scan(&changes)
	if changes != 1 {
		t.Errorf("config_change rows %d", changes)
	}
	if w := call(f, "POST", path, `{"packs":["../skills"]}`, admin); w.Code != 400 || decode(t, w)["error"] != "Could not package pack ../skills." {
		t.Errorf("bad pack id: %d %s", w.Code, w.Body.String())
	}

	wk.updates = func() (int, string) { return 500, `{"error":"Connect a remote repository first."}` }
	if w := call(f, "GET", path, "", admin); w.Code != 400 || decode(t, w)["error"] != "Connect a remote repository first." {
		t.Errorf("worker refusal: %d %s", w.Code, w.Body.String())
	}
	wk.updates = func() (int, string) { return 503, `<html>` }
	if w := call(f, "GET", path, "", admin); w.Code != 400 || decode(t, w)["error"] != "Update check failed (503)" {
		t.Errorf("worker text reply: %d %s", w.Code, w.Body.String())
	}
	f.API.Gate.WorkerURL = "http://127.0.0.1:1"
	if w := call(f, "GET", path, "", admin); w.Code != 502 || w.Body.String() != `{"error":"worker is unavailable"}` {
		t.Errorf("worker down: %d %s", w.Code, w.Body.String())
	}
	if got := remoteAudit(t, f); got != "context:published,context:pulled" {
		t.Errorf("audit %s", got)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenPullRequest(t *testing.T) {
	var seen *http.Request
	var seenBody string
	status, reply := 201, `{"html_url":"https://github.com/acme/ctx/pull/7"}`
	saved := remoteHTTP
	remoteHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		seen = r
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(reply)), Header: http.Header{}}, nil
	})}
	t.Cleanup(func() { remoteHTTP = saved })

	gh, _ := parseRemoteURL("git@github.com:acme/ctx.git")
	link, err := openPullRequest(remoteAccess{url: gh, token: "tok"}, "openneko/context-1", "main", "T", "B")
	if err != nil || link != "https://github.com/acme/ctx/pull/7" || seen.URL.String() != "https://api.github.com/repos/acme/ctx/pulls" ||
		seen.Header.Get("Authorization") != "Bearer tok" || seenBody != `{"base":"main","body":"B","head":"openneko/context-1","title":"T"}` {
		t.Errorf("github: %q %v %s %s", link, err, seen.URL, seenBody)
	}
	status, reply = 403, `{"message":"Resource not accessible"}`
	_, err = openPullRequest(remoteAccess{url: gh, token: "tok"}, "openneko/context-1", "main", "T", "B")
	re, ok := err.(remoteError)
	if !ok || re.msg != "The branch was pushed, but GitHub did not open the request (403: Resource not accessible). Give the token Pull requests: Read and write on this repository, or open the request yourself." ||
		re.link != "https://github.com/acme/ctx/compare/main...openneko%2Fcontext-1?expand=1" {
		t.Errorf("github refused: %#v", err)
	}

	gl, _ := parseRemoteURL("https://gitlab.example.com/grp/sub/ctx.git")
	status, reply = 500, `{}`
	_, err = openPullRequest(remoteAccess{url: gl, token: "tok"}, "openneko/context-1", "main", "T", "B")
	re, _ = err.(remoteError)
	if seen.URL.String() != "https://gitlab.example.com/api/v4/projects/grp%2Fsub%2Fctx/merge_requests" ||
		re.msg != "The branch was pushed, but GitLab did not open the request (500). Open the request yourself." ||
		re.link != "https://gitlab.example.com/grp/sub/ctx/-/merge_requests/new?merge_request%5Bsource_branch%5D=openneko%2Fcontext-1&merge_request%5Btarget_branch%5D=main" {
		t.Errorf("gitlab: %s %#v", seen.URL, err)
	}
	other, _ := parseRemoteURL("https://git.acme.local/a/b.git")
	if link, err := openPullRequest(remoteAccess{url: other, token: "tok"}, "h", "main", "T", "B"); link != "" || err != nil {
		t.Errorf("other host: %q %v", link, err)
	}
}

func TestGitAuthKeepsTheTokenOut(t *testing.T) {
	u, _ := parseRemoteURL("https://gitlab.com/a/b.git")
	access := remoteAccess{url: u, token: "glpat-xyz"}
	env, cleanup, err := gitAuth(access)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got := strings.Join(env, " "); !strings.Contains(got, "GIT_CONFIG_VALUE_0=Authorization: Basic b2F1dGgyOmdscGF0LXh5eg==") || strings.Contains(got, "glpat-xyz") {
		t.Errorf("env %q", got)
	}
	if got := scrubToken("fatal: glpat-xyz and b2F1dGgyOmdscGF0LXh5eg==", access); got != "fatal: *** and ***" {
		t.Errorf("scrub %q", got)
	}
	sshURL, _ := parseRemoteURL("ssh://git@host/a/b.git")
	if _, _, err := gitAuth(remoteAccess{url: sshURL}); err == nil || err.Error() != "This SSH remote has no deploy key." {
		t.Errorf("ssh without key: %v", err)
	}
	if u, _ := parseRemoteURL("git@github.com:acme/ctx.git"); u.String() != "ssh://git@github.com/acme/ctx.git" {
		t.Errorf("short form %s", u)
	}
}
