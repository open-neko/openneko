package webfront

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	contextRemoteKinds        = []string{"skills", "skill-overlays", "workflows", "memory"}
	defaultContextRemoteKinds = []string{"skills", "skill-overlays", "workflows"}
	shortSSHRemote            = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9.-]+):([^\s/][^\s]*)$`)
	branchNamePattern         = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]{0,199}$`)
	missingRemoteRef          = regexp.MustCompile(`(?i)couldn't find remote ref`)
	pushRejected              = regexp.MustCompile(`(?i)non-fast-forward|fetch first|rejected`)
	allowFileRemotes          = false
	remoteHTTP                = &http.Client{Timeout: 30 * time.Second}
	gitNetworkTimeout         = 2 * time.Minute
)

const (
	emptyTree       = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	itemDiffLimit   = 200_000
	packArchiveSize = 32 << 20
)

// remoteError is ContextRemoteError: its message goes to the administrator.
// link is set when a branch was pushed and only the pull request is missing.
type remoteError struct{ msg, link string }

func (e remoteError) Error() string { return e.msg }

// parseRemoteURL is parseRemoteUrl() in config-vcs/remote.ts.
func parseRemoteURL(value string) (*url.URL, error) {
	text := strings.TrimSpace(value)
	if m := shortSSHRemote.FindStringSubmatch(text); m != nil {
		text = "ssh://" + m[1] + "@" + m[2] + "/" + m[3]
	}
	u, err := url.Parse(text)
	if err != nil || u.Scheme == "" || u.Opaque != "" || (u.Host == "" && u.Scheme != "file") {
		return nil, remoteError{msg: "Enter the repository's address, such as https://github.com/acme/openneko-context.git or git@github.com:acme/openneko-context.git."}
	}
	if u.Scheme == "file" && allowFileRemotes {
		return u, nil
	}
	if u.Scheme != "https" && u.Scheme != "ssh" {
		return nil, remoteError{msg: "The repository address must start with https:// or ssh://, or have the form git@host:org/repo.git."}
	}
	if _, hasPassword := u.User.Password(); hasPassword || (u.Scheme == "https" && u.User != nil && u.User.Username() != "") {
		return nil, remoteError{msg: "Put the access token in the token field, not in the address."}
	}
	segments := 0
	for _, part := range strings.Split(u.Path, "/") {
		if part != "" {
			segments++
		}
	}
	if u.RawQuery != "" || u.Fragment != "" || segments < 2 {
		return nil, remoteError{msg: "The address must name a repository, such as https://github.com/acme/openneko-context.git."}
	}
	u.ForceQuery = false
	if u.Scheme == "https" {
		u.Host = strings.ToLower(u.Host)
		if u.Port() == "443" {
			u.Host = u.Hostname()
		}
	}
	return u, nil
}

func remoteTransport(u *url.URL) string {
	switch u.Scheme {
	case "ssh":
		return "ssh"
	case "file":
		return "file"
	}
	return "https"
}

func remoteProvider(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "github.com":
		return "github"
	case host == "gitlab.com" || strings.HasPrefix(host, "gitlab."):
		return "gitlab"
	}
	return "other"
}

func validBranchName(branch string) bool {
	return branchNamePattern.MatchString(branch) && !strings.Contains(branch, "..") &&
		!strings.HasSuffix(branch, "/") && !strings.HasSuffix(branch, ".lock")
}

func repositoryPath(u *url.URL) string {
	return strings.TrimRight(strings.TrimSuffix(strings.TrimLeft(u.Path, "/"), ".git"), "/")
}

// localOnlyPath is a path with a segment that starts with ".", such as a
// deploy marker. It is never published or compared.
func localOnlyPath(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

type remoteAccess struct {
	url                *url.URL
	token, username    string
	sshKey, knownHosts string
}

// gitCmd runs git in root with the env of git() in config-vcs/git-shell.ts.
// A network command gets a timeout so a silent remote cannot hold the lock.
func gitCmd(root string, env []string, input string, network bool, args ...string) (string, error) {
	ctx := context.Background()
	if network {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gitNetworkTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=OpenNeko", "GIT_AUTHOR_EMAIL=auto@openneko.local",
		"GIT_COMMITTER_NAME=OpenNeko", "GIT_COMMITTER_EMAIL=auto@openneko.local",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Env = append(cmd.Env, env...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s failed: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

func gitOut(root string, args ...string) (string, error) {
	return gitCmd(root, nil, "", false, args...)
}

func gitRef(root, ref string) string {
	out, err := gitOut(root, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// gitAuth is withGitAuth(): the env that authenticates git, with no
// credential in an argument. The caller runs cleanup when git is done.
func gitAuth(access remoteAccess) ([]string, func(), error) {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="}
	if remoteTransport(access.url) == "ssh" {
		if access.sshKey == "" {
			return nil, nil, remoteError{msg: "This SSH remote has no deploy key."}
		}
		sshEnv, cleanup, err := sshGitEnv(access.sshKey, access.knownHosts)
		if err != nil {
			return nil, nil, err
		}
		return append(env, sshEnv...), cleanup, nil
	}
	if access.token == "" {
		return env, func() {}, nil
	}
	return append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(gitUser(access)+":"+access.token))), func() {}, nil
}

func gitUser(access remoteAccess) string {
	switch {
	case access.username != "":
		return access.username
	case remoteProvider(access.url) == "gitlab":
		return "oauth2"
	}
	return "x-access-token"
}

func scrubToken(message string, access remoteAccess) string {
	if access.token == "" {
		return message
	}
	for _, secret := range []string{
		base64.StdEncoding.EncodeToString([]byte(gitUser(access) + ":" + access.token)),
		access.token,
		base64.StdEncoding.EncodeToString([]byte(access.token)),
	} {
		message = strings.ReplaceAll(message, secret, "***")
	}
	return message
}

// fetchRemote is fetchRemote() in config-vcs/remote.ts: the remote branch
// tip in refs/openneko/remote/<branch>, or "" when the branch is missing.
func fetchRemote(root string, access remoteAccess, branch string) (string, error) {
	env, cleanup, err := gitAuth(access)
	if err != nil {
		return "", err
	}
	defer cleanup()
	tracking := "refs/openneko/remote/" + branch
	if _, err := gitCmd(root, env, "", true, "fetch", "--no-tags", "--quiet", access.url.String(), "+refs/heads/"+branch+":"+tracking); err != nil {
		if missingRemoteRef.MatchString(err.Error()) {
			return "", nil
		}
		return "", remoteError{msg: scrubToken(err.Error(), access)}
	}
	out, err := gitOut(root, "rev-parse", tracking)
	return strings.TrimSpace(out), err
}

type treeEntry struct{ line, sha, path string }

func treeEntries(root, ref string, paths ...string) ([]treeEntry, error) {
	args := []string{"ls-tree", "-r", "--full-tree", ref}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	out, err := gitOut(root, args...)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		meta, path, _ := strings.Cut(line, "\t")
		fields := strings.Split(meta, " ")
		sha := ""
		if len(fields) > 2 {
			sha = fields[2]
		}
		entries = append(entries, treeEntry{line, sha, path})
	}
	return entries, nil
}

// skillTreeFiles is the files of one skill tree object, keyed by the path
// inside the skill.
func skillTreeFiles(root, tree string) map[string]string {
	files := map[string]string{}
	if tree == "" {
		return files
	}
	out, _ := gitOut(root, "ls-tree", "-r", tree)
	for _, line := range strings.Split(out, "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		if fields := strings.Split(meta, " "); ok && path != "" && !localOnlyPath(path) && len(fields) > 2 {
			files[path] = fields[2]
		}
	}
	return files
}

// folderTrees is the tree id of each top-level folder under prefix at ref.
func folderTrees(root, ref, prefix string) map[string]string {
	trees := map[string]string{}
	out, _ := gitOut(root, "ls-tree", ref, "--", prefix+"/")
	for _, line := range strings.Split(out, "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		fields := strings.Split(meta, " ")
		if ok && len(fields) > 2 && fields[1] == "tree" && path != "" {
			trees[path[len(prefix)+1:]] = fields[2]
		}
	}
	return trees
}

// skillBase is a skill's state at its last sync with a remote: the
// repository's tree, and OpenNeko's own tree. An older record is a string
// that holds only the repository's tree.
type skillBase struct {
	Remote string `json:"remote"`
	Local  string `json:"local,omitempty"`
}

func readSkillBases(raw []byte) map[string]any {
	bases := map[string]any{}
	_ = json.Unmarshal(raw, &bases)
	if bases == nil {
		bases = map[string]any{}
	}
	return bases
}

func skillBaseOf(v any) (skillBase, bool) {
	switch b := v.(type) {
	case string:
		return skillBase{Remote: b}, true
	case map[string]any:
		remote, _ := b["remote"].(string)
		local, _ := b["local"].(string)
		return skillBase{Remote: remote, Local: local}, true
	}
	return skillBase{}, false
}

type publishOptions struct {
	root, branch string
	access       remoteAccess
	kinds        []string
	exclude      map[string]bool
	bases        map[string]any
}

// publishTree is publishTree() in config-vcs/remote.ts: the remote tree with
// OpenNeko's copy of each published folder. Files outside those folders
// stay. A file that only the remote has stays, unless OpenNeko had it at
// the skill's last sync and then removed it.
func publishTree(opts publishOptions, head, base string) (string, error) {
	scratch, err := os.MkdirTemp("", "neko-export-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
	published := func(path string) bool {
		for _, kind := range opts.kinds {
			if strings.HasPrefix(path, kind+"/") {
				return true
			}
		}
		return false
	}
	var remote []treeEntry
	if base != "" {
		if remote, err = treeEntries(opts.root, base); err != nil {
			return "", err
		}
	}
	all, err := treeEntries(opts.root, head, opts.kinds...)
	if err != nil {
		return "", err
	}
	var local []treeEntry
	for _, e := range all {
		if !localOnlyPath(e.path) {
			local = append(local, e)
		}
	}
	var lines []string
	for _, e := range remote {
		if !published(e.path) {
			lines = append(lines, e.line)
		}
	}
	skillName := func(path string) string { return strings.Split(path, "/")[1] }
	for _, kind := range opts.kinds {
		ours := map[string]bool{}
		for _, e := range local {
			if !strings.HasPrefix(e.path, kind+"/") {
				continue
			}
			if kind == "skills" && (len(strings.Split(e.path, "/")) <= 2 || opts.exclude[skillName(e.path)]) {
				continue
			}
			lines = append(lines, e.line)
			ours[e.path] = true
		}
		if kind != "skills" {
			continue
		}
		synced := map[string]map[string]string{}
		for _, e := range remote {
			if !strings.HasPrefix(e.path, "skills/") || ours[e.path] {
				continue
			}
			name := skillName(e.path)
			if _, ok := synced[name]; !ok {
				b, _ := skillBaseOf(opts.bases[name])
				synced[name] = skillTreeFiles(opts.root, b.Local)
			}
			_, hadIt := synced[name][strings.TrimPrefix(e.path, "skills/"+name+"/")]
			if opts.exclude[name] || localOnlyPath(e.path) || !hadIt {
				lines = append(lines, e.line)
			}
		}
	}
	if _, err := gitCmd(opts.root, env, "", false, "read-tree", "--empty"); err != nil {
		return "", err
	}
	if len(lines) > 0 {
		if _, err := gitCmd(opts.root, env, strings.Join(lines, "\n")+"\n", false, "update-index", "--index-info"); err != nil {
			return "", err
		}
	}
	out, err := gitCmd(opts.root, env, "", false, "write-tree")
	return strings.TrimSpace(out), err
}

// publishTrees is the remote branch's tree and the tree a publish would
// send, or ok false when the org has no version yet. Caller holds the lock.
func publishTrees(opts publishOptions) (baseTree, tree string, ok bool, err error) {
	head := gitRef(opts.root, "HEAD")
	if head == "" {
		return "", "", false, nil
	}
	base, err := fetchRemote(opts.root, opts.access, opts.branch)
	if err != nil {
		return "", "", false, err
	}
	if tree, err = publishTree(opts, head, base); err != nil {
		return "", "", false, err
	}
	baseTree = emptyTree
	if base != "" {
		out, err := gitOut(opts.root, "rev-parse", base+"^{tree}")
		if err != nil {
			return "", "", false, err
		}
		baseTree = strings.TrimSpace(out)
	}
	return baseTree, tree, true, nil
}

type publishPreviewItem struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Added   int    `json:"added"`
	Changed int    `json:"changed"`
	Removed int    `json:"removed"`
}

// previewPublish is what a publish would send, grouped by item.
func previewPublish(opts publishOptions) ([]publishPreviewItem, error) {
	configRepoMu.Lock()
	defer configRepoMu.Unlock()
	items := []publishPreviewItem{}
	baseTree, tree, ok, err := publishTrees(opts)
	if err != nil || !ok {
		return items, err
	}
	out, err := gitOut(opts.root, "diff-tree", "-r", "--name-status", "--no-renames", baseTree, tree)
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		status, path, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		parts := strings.Split(path, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" || !slices.Contains(opts.kinds, parts[0]) {
			continue
		}
		kind, name := parts[0], parts[1]
		if kind != "skills" {
			name = strings.TrimSuffix(name, ".md")
		}
		key := kind + "/" + name
		i, seen := index[key]
		if !seen {
			i = len(items)
			index[key] = i
			items = append(items, publishPreviewItem{Kind: kind, Name: name})
		}
		switch status {
		case "A":
			items[i].Added++
		case "D":
			items[i].Removed++
		default:
			items[i].Changed++
		}
	}
	slices.SortFunc(items, func(a, b publishPreviewItem) int { return localeCompare(a.Kind+"/"+a.Name, b.Kind+"/"+b.Name) })
	return items, nil
}

type itemDiff struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

func cappedDiff(diff string) itemDiff {
	if len(diff) <= itemDiffLimit {
		return itemDiff{Diff: diff}
	}
	cut := itemDiffLimit
	for cut > 0 && !utf8.RuneStart(diff[cut]) {
		cut--
	}
	return itemDiff{Diff: diff[:cut], Truncated: true}
}

func itemPaths(kind, name string) ([]string, error) {
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "/") {
		return nil, remoteError{msg: "Unknown item."}
	}
	if kind == "skills" {
		return []string{"skills/" + name}, nil
	}
	return []string{kind + "/" + name + ".md", kind + "/" + name}, nil
}

// publishItemDiff is the unified diff a publish would send for one item.
func publishItemDiff(opts publishOptions, kind, name string) (itemDiff, error) {
	paths, err := itemPaths(kind, name)
	if err != nil {
		return itemDiff{}, err
	}
	configRepoMu.Lock()
	defer configRepoMu.Unlock()
	baseTree, tree, ok, err := publishTrees(opts)
	if err != nil || !ok {
		return itemDiff{}, err
	}
	out, err := gitOut(opts.root, append([]string{"diff", "--no-color", "--no-renames", baseTree, tree, "--"}, paths...)...)
	if err != nil {
		return itemDiff{}, err
	}
	return cappedDiff(out), nil
}

// incomingSkillDiff is the repository's version of a skill compared with
// OpenNeko's, from the tip that the last update check fetched.
func incomingSkillDiff(root, branch, name string) (itemDiff, error) {
	paths, err := itemPaths("skills", name)
	if err != nil {
		return itemDiff{}, err
	}
	configRepoMu.Lock()
	defer configRepoMu.Unlock()
	tip := gitRef(root, "refs/openneko/remote/"+branch)
	if tip == "" {
		return itemDiff{}, remoteError{msg: "Check for updates first."}
	}
	path := paths[0]
	out, err := gitOut(root, "diff", "--no-color", "--no-renames", "HEAD", tip, "--", path,
		":(exclude,glob)"+path+"/**/.*", ":(exclude,glob)"+path+"/**/.*/**")
	if err != nil {
		return itemDiff{}, err
	}
	return cappedDiff(out), nil
}

type publishResult struct {
	Status string  `json:"status"`
	Branch string  `json:"branch"`
	SHA    *string `json:"sha"`
	Link   *string `json:"link"`
	Detail string  `json:"detail"`
	Head   string  `json:"head,omitempty"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// publishContext is publishContext() in config-vcs/remote.ts. The new
// commit's parent is the remote branch tip, so a push is a fast-forward and
// a pull request shows only what OpenNeko changed.
func publishContext(opts publishOptions, mode string, now time.Time) (publishResult, error) {
	configRepoMu.Lock()
	defer configRepoMu.Unlock()
	env, cleanup, err := gitAuth(opts.access)
	if err != nil {
		return publishResult{}, err
	}
	defer cleanup()
	root, access := opts.root, opts.access
	remote := access.url.String()
	network := func(args ...string) error {
		if _, err := gitCmd(root, env, "", true, args...); err != nil {
			return remoteError{msg: scrubToken(err.Error(), access)}
		}
		return nil
	}
	head := gitRef(root, "HEAD")
	if head == "" {
		return publishResult{}, remoteError{msg: "There is nothing to publish yet."}
	}
	tracking := "refs/openneko/remote/" + opts.branch
	base := ""
	if err := network("fetch", "--no-tags", "--quiet", remote, "+refs/heads/"+opts.branch+":"+tracking); err != nil {
		if !missingRemoteRef.MatchString(err.Error()) {
			return publishResult{}, err
		}
	} else {
		out, err := gitOut(root, "rev-parse", tracking)
		if err != nil {
			return publishResult{}, err
		}
		base = strings.TrimSpace(out)
	}
	tree, err := publishTree(opts, head, base)
	if err != nil {
		return publishResult{}, err
	}
	if base != "" {
		if out, err := gitOut(root, "rev-parse", base+"^{tree}"); err != nil {
			return publishResult{}, err
		} else if strings.TrimSpace(out) == tree {
			return publishResult{Status: "unchanged", Branch: opts.branch, SHA: &base, Detail: "The remote already has this version.", Head: head}, nil
		}
	}
	title := "Update OpenNeko context"
	body := "Published from OpenNeko version " + head[:12] + ": " + strings.Join(opts.kinds, ", ") + "."
	args := []string{"commit-tree", tree}
	if base != "" {
		args = append(args, "-p", base)
	}
	out, err := gitOut(root, append(args, "-m", title, "-m", body)...)
	if err != nil {
		return publishResult{}, err
	}
	sha := strings.TrimSpace(out)

	if mode == "push" || base == "" {
		if err := network("push", "--quiet", remote, sha+":refs/heads/"+opts.branch); err != nil {
			if pushRejected.MatchString(err.Error()) {
				return publishResult{}, remoteError{msg: "The remote branch changed while OpenNeko was publishing. Publish again."}
			}
			return publishResult{}, err
		}
		if _, err := gitOut(root, "update-ref", tracking, sha); err != nil {
			return publishResult{}, err
		}
		var link *string
		if remoteProvider(access.url) == "github" {
			link = optional("https://github.com/" + repositoryPath(access.url) + "/tree/" + opts.branch)
		}
		detail := "Created " + opts.branch + " on the remote."
		if base != "" {
			detail = "Pushed to " + opts.branch + "."
		}
		return publishResult{Status: "pushed", Branch: opts.branch, SHA: &sha, Link: link, Detail: detail, Head: head}, nil
	}

	headBranch := "openneko/context-" + now.UTC().Format("20060102-150405")
	if err := network("push", "--quiet", remote, sha+":refs/heads/"+headBranch); err != nil {
		return publishResult{}, err
	}
	link, err := openPullRequest(access, headBranch, opts.branch, title, body)
	if err != nil {
		return publishResult{}, err
	}
	detail := "Pushed " + headBranch + ". Open a request to merge it into " + opts.branch + " on your git host."
	if link != "" {
		detail = "Opened a request to merge " + headBranch + " into " + opts.branch + "."
	}
	return publishResult{Status: "pull_request", Branch: headBranch, SHA: &sha, Link: optional(link), Detail: detail}, nil
}

func webOrigin(u *url.URL) string {
	if remoteTransport(u) == "ssh" {
		return "https://" + u.Hostname()
	}
	return "https://" + u.Host
}

// openPullRequest is openPullRequest() in config-vcs/remote.ts: the
// request's link, or "" when the host or the token cannot open one.
func openPullRequest(access remoteAccess, head, base, title, body string) (string, error) {
	provider := remoteProvider(access.url)
	if access.token == "" || provider == "other" {
		return "", nil
	}
	path := repositoryPath(access.url)
	var target string
	var payload map[string]string
	header := http.Header{"Authorization": {"Bearer " + access.token}, "Content-Type": {"application/json"}}
	if provider == "github" {
		target = "https://api.github.com/repos/" + path + "/pulls"
		payload = map[string]string{"title": title, "head": head, "base": base, "body": body}
		header.Set("Accept", "application/vnd.github+json")
		header.Set("User-Agent", "OpenNeko")
	} else {
		target = webOrigin(access.url) + "/api/v4/projects/" + encodeURIComponent(path) + "/merge_requests"
		payload = map[string]string{"title": title, "source_branch": head, "target_branch": base, "description": body}
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header = header
	res, err := remoteHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var reply struct {
		HTMLURL string `json:"html_url"`
		WebURL  string `json:"web_url"`
		Message string `json:"message"`
	}
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	_ = json.Unmarshal(data, &reply)
	status, message := res.StatusCode, reply.Message
	if status >= 200 && status < 300 {
		if reply.HTMLURL != "" {
			return reply.HTMLURL, nil
		}
		return reply.WebURL, nil
	}
	host, permission := "GitHub", "Pull requests: Read and write"
	if provider == "gitlab" {
		host, permission = "GitLab", "the api scope"
	}
	text := fmt.Sprintf("The branch was pushed, but %s did not open the request (%d", host, status)
	if message != "" {
		text += ": " + message
	}
	text += ")."
	if status == 401 || status == 403 || status == 404 {
		text += " Give the token " + permission + " on this repository, or open the request yourself."
	} else {
		text += " Open the request yourself."
	}
	return "", remoteError{msg: text, link: newRequestURL(access.url, provider, path, head, base)}
}

func newRequestURL(u *url.URL, provider, path, head, base string) string {
	origin := webOrigin(u)
	if provider == "github" {
		return origin + "/" + path + "/compare/" + encodeURIComponent(base) + "..." + encodeURIComponent(head) + "?expand=1"
	}
	q := url.Values{"merge_request[source_branch]": {head}, "merge_request[target_branch]": {base}}
	return origin + "/" + path + "/-/merge_requests/new?" + q.Encode()
}

// remotePackArchive is the ZIP the pack uploader accepts: one top-level
// folder named after the pack.
func remotePackArchive(root, tip, id string) ([]byte, error) {
	fail := remoteError{msg: "Could not package pack " + id + "."}
	if !packIDPattern.MatchString(id) {
		return nil, fail
	}
	cmd := exec.Command("git", "archive", "--format=zip", "--prefix="+id+"/", tip+":packs/"+id)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil || out.Len() > packArchiveSize {
		return nil, fail
	}
	return out.Bytes(), nil
}

// bringInRemoteSkills replaces each named skill with the remote tip's copy
// and commits it as one version. It returns the new HEAD, or "" when
// nothing changed.
func bringInRemoteSkills(root, tip string, names []string, message string) (string, error) {
	if len(names) == 0 {
		return "", nil
	}
	configRepoMu.Lock()
	defer configRepoMu.Unlock()
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = "skills/" + name
	}
	head := gitRef(root, "HEAD")
	var keep []string
	if head != "" {
		entries, err := treeEntries(root, head, paths...)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if localOnlyPath(e.path) {
				keep = append(keep, e.path)
			}
		}
	}
	steps := [][]string{
		append([]string{"rm", "-r", "-q", "--ignore-unmatch", "--"}, paths...),
		append([]string{"checkout", tip, "--"}, paths...),
	}
	if len(keep) > 0 {
		steps = append(steps, append([]string{"checkout", head, "--"}, keep...))
	}
	for _, args := range steps {
		if _, err := gitOut(root, args...); err != nil {
			return "", err
		}
	}
	status, err := gitOut(root, append([]string{"status", "--porcelain", "--"}, paths...)...)
	if err != nil || strings.TrimSpace(status) == "" {
		return "", err
	}
	if _, err := gitOut(root, append([]string{"commit", "-q", "-m", message, "--"}, paths...)...); err != nil {
		return "", err
	}
	return gitRef(root, "HEAD"), nil
}

// unmodifiedBuiltinSkills is unmodifiedBuiltinSkills() in work/workspace.ts:
// built-in skills in a workspace that nobody has changed.
func unmodifiedBuiltinSkills(skillsRoot string) map[string]bool {
	names := map[string]bool{}
	for _, name := range bundledSkillNames() {
		hash, err := fingerprintSkillTree(filepath.Join(skillsRoot, name))
		if err != nil {
			continue
		}
		if origin := readSkillOrigin(filepath.Join(skillsRoot, name)); origin != nil {
			names[name] = origin.SourceHash == hash
			continue
		}
		if bundled, err := fingerprintSkillTree(bundledSkillDir(name)); err == nil && bundled == hash {
			names[name] = true
		}
	}
	return names
}
