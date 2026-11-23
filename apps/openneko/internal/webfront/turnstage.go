package webfront

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/appdir"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

// stageBootID keeps this process's stages apart from those of an earlier
// start, which the sweep deletes.
var stageBootID = func() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}()

var unsafeStageSegment = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func stageSegment(v string) string {
	v = unsafeStageSegment.ReplaceAllString(v, "_")
	if len(v) > 64 {
		v = v[:64]
	}
	return v
}

// AgentRunsDir is the folder this host shares with serve.
func AgentRunsDir() string { return agentRunsRoot() }

func agentRunsRoot() string {
	if v := strings.TrimSpace(os.Getenv("OPENNEKO_AGENT_RUNS_DIR")); v != "" {
		return v
	}
	return filepath.Join(appdir.Data(), "agent-runs")
}

// stageOwner differs from the Node launcher's owner, so that neither host
// sweeps the other's live stages.
const stageOwner = "openneko-turn"

// SweepStages is reapStrandedRuns(): it deletes this host's stages from an
// earlier start, which a restart in the middle of a turn leaves behind.
func SweepStages() ([]string, error) {
	root := agentRunsRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	prefix := stageOwner + "."
	var gone []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && !strings.HasPrefix(name, prefix+stageBootID+".") {
			if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
				return gone, err
			}
			gone = append(gone, name)
		}
	}
	return gone, nil
}

// SweepStagesEvery runs SweepStages at once and then at each interval until
// the context ends.
func SweepStagesEvery(ctx context.Context, every time.Duration) {
	sweep := func() {
		gone, err := SweepStages()
		if err != nil {
			log.Printf("[agent-run] could not delete agent run folders left by an earlier start: %v", err)
		} else if len(gone) > 0 {
			log.Printf("[agent-run] deleted %d agent run folders left by an earlier start", len(gone))
		}
	}
	sweep()
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()
}

// runnerGuard counts the serve turns on a stage whose runner serve has not
// yet reported dead. Serve sends a turn's exit frame only after it killed
// the runner's process group.
type runnerGuard struct {
	mu   sync.Mutex
	open int
}

func (g *runnerGuard) start() {
	if g != nil {
		g.mu.Lock()
		g.open++
		g.mu.Unlock()
	}
}

func (g *runnerGuard) ended() {
	if g != nil {
		g.mu.Lock()
		g.open--
		g.mu.Unlock()
	}
}

func (g *runnerGuard) settled() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open == 0
}

// turnStage is the least the run needs, copied under the shared runs
// folder: the knowledge pack, the thread's uploads, the run folder, the
// run's skills and the person's library uploads. Memory, other threads and other runs stay out.
type turnStage struct {
	Dir       string
	Workspace agentWorkspace
	host      agentWorkspace
	baseline  skillFileHashes
	staged    skillFileHashes
	actor     runActorRow
	own       map[string]bool

	captureReason string
}

type stageOptions struct {
	Allowed  []string
	Own      []string
	Terminal bool
	Actor    runActorRow
	Library  func(stageOrg string) error
}

func stagePath(host agentWorkspace, stageOrg, p string) (string, error) {
	rel, err := filepath.Rel(host.OrgRoot, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("agent workspace path escapes org root: %s", p)
	}
	return filepath.Join(stageOrg, rel), nil
}

func copyIfPresent(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return copyTree(src, dst)
}

// stageTurn stages the company's changed skills, the built-in skills the run
// may use, then the member's own skills in the run's scope.
func stageTurn(host agentWorkspace, o stageOptions) (st *turnStage, err error) {
	root := agentRunsRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, stageOwner+"."+stageBootID+".")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	stageOrg := filepath.Join(dir, "workspace", filepath.Base(host.OrgRoot))
	ws := agentWorkspace{runners: &runnerGuard{}}
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&ws.OrgRoot, host.OrgRoot}, {&ws.SkillsRoot, host.SkillsRoot}, {&ws.MemoryRoot, host.MemoryRoot},
		{&ws.KnowledgeRoot, host.KnowledgeRoot}, {&ws.UploadsRoot, host.UploadsRoot}, {&ws.RunsRoot, host.RunsRoot},
		{&ws.ThreadUploadsRoot, host.ThreadUploadsRoot}, {&ws.RunRoot, host.RunRoot}, {&ws.ArtifactRoot, host.ArtifactRoot},
		{&ws.BinRoot, host.BinRoot},
	} {
		if *f.dst, err = stagePath(host, stageOrg, f.src); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(stageOrg, 0o755); err != nil {
		return nil, err
	}
	if snap := readKnowledgeSnapshot(host.KnowledgeRoot); snap != nil {
		if err := os.MkdirAll(ws.KnowledgeRoot, 0o755); err != nil {
			return nil, err
		}
		for _, name := range knowledgeSnapshotFiles {
			if err := os.WriteFile(filepath.Join(ws.KnowledgeRoot, name), []byte(snap.Files[name]), 0o644); err != nil {
				return nil, err
			}
		}
	} else if err := copyIfPresent(host.KnowledgeRoot, ws.KnowledgeRoot); err != nil {
		return nil, err
	}
	if err := copyIfPresent(host.ThreadUploadsRoot, ws.ThreadUploadsRoot); err != nil {
		return nil, err
	}
	if err := copyIfPresent(host.RunRoot, ws.RunRoot); err != nil {
		return nil, err
	}
	for _, d := range []string{ws.OrgRoot, ws.SkillsRoot, ws.MemoryRoot, ws.KnowledgeRoot, ws.UploadsRoot, ws.RunsRoot,
		ws.ThreadUploadsRoot, ws.RunRoot, ws.ArtifactRoot, ws.BinRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := copySkillOverrides(host.SkillsRoot, ws.SkillsRoot, o.Allowed, o.Terminal); err != nil {
		return nil, err
	}
	if err := removeRetiredBuiltins(ws.SkillsRoot); err != nil {
		return nil, err
	}
	if err := seedBuiltinSkills(ws.SkillsRoot, func(name string) bool {
		return (o.Allowed == nil || slices.Contains(o.Allowed, name)) && (!sandboxSkills[name] || o.Terminal)
	}); err != nil {
		return nil, err
	}
	baseline := hashSkillTree(host.SkillsRoot)
	own, err := stageOwnSkills(memberSkillsRoot(host.OrgRoot, o.Actor.UserID), ws.SkillsRoot, o, baseline)
	if err != nil {
		return nil, err
	}
	staged := hashSkillTree(ws.SkillsRoot)
	if o.Library != nil {
		if err := o.Library(stageOrg); err != nil {
			return nil, err
		}
	}
	if err := shareStageWithGroup(dir); err != nil {
		return nil, err
	}
	if err := lockRunLibrary(stageOrg); err != nil {
		return nil, err
	}
	return &turnStage{Dir: dir, Workspace: ws, host: host, baseline: baseline, staged: staged, actor: o.Actor, own: own}, nil
}

// stageOwnSkills copies each own skill in the run's scope over the staged
// skill of the same name, and puts its files in the baseline in place of
// the team skill's files.
func stageOwnSkills(ownRoot, dst string, o stageOptions, baseline skillFileHashes) (map[string]bool, error) {
	staged := map[string]bool{}
	if o.Actor.UserID == "" {
		return staged, nil
	}
	for _, name := range o.Own {
		src := filepath.Join(ownRoot, name)
		if !validSkillName(name) || o.Allowed != nil && !slices.Contains(o.Allowed, name) || sandboxSkills[name] && !o.Terminal || !hasSkillFiles(src) {
			continue
		}
		dest := filepath.Join(dst, name)
		if err := os.RemoveAll(dest); err != nil {
			return nil, err
		}
		if err := copySkillFolder(src, dest, ""); err != nil {
			return nil, err
		}
		staged[name] = true
	}
	if len(staged) == 0 {
		return staged, nil
	}
	for p := range baseline {
		if staged[skillOf(p)] {
			delete(baseline, p)
		}
	}
	for p, hash := range hashSkillTree(dst) {
		if staged[skillOf(p)] {
			baseline[p] = hash
		}
	}
	return staged, nil
}

// copySkillOverrides is copySkillOverrides() in packages/llm/src/work/workspace.ts:
// each skill the run may use and that differs from its built-in copy, with
// its learned guidance. Unchanged built-in skills come from the image.
func copySkillOverrides(src, dst string, allowed []string, terminal bool) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return localeCompare(a.Name(), b.Name()) })
	orgRoot := filepath.Dir(src)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || allowed != nil && !slices.Contains(allowed, name) || sandboxSkills[name] && !terminal {
			continue
		}
		source := filepath.Join(src, name)
		bundled := bundledSkillDir(name)
		unmodified := false
		hash, herr := fingerprintSkillTree(source)
		if herr == nil && bundled != "" {
			if image, err := fingerprintSkillTree(bundled); err == nil {
				if origin := readSkillOrigin(source); origin != nil {
					unmodified = origin.SourceHash == hash
				} else {
					unmodified = hash == image
				}
			}
		}
		if _, live := learnedOverlayFor(orgRoot, name, hash); unmodified && !live {
			continue
		}
		base := source
		if unmodified {
			base = bundled
		}
		dest := filepath.Join(dst, name)
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		if err := copyTree(base, dest); err != nil {
			return err
		}
		baseHash, err := fingerprintSkillTree(base)
		if err != nil {
			return err
		}
		if overlay, live := learnedOverlayFor(orgRoot, name, baseHash); live {
			if err := appendLearnedGuidance(filepath.Join(dest, "SKILL.md"), overlay); err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	learnedStart = "<!-- openneko-learned:start -->"
	learnedEnd   = "<!-- openneko-learned:end -->"
)

// learnedOverlayFor is the body of the skill's learned guidance, and
// whether it applies to the base hash.
func learnedOverlayFor(orgRoot, name, baseHash string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(orgRoot, "skill-overlays", name, "LEARNED.md"))
	if err != nil {
		return "", false
	}
	m := learnedFile.FindStringSubmatch(string(raw))
	if m == nil {
		return "", false
	}
	status := learnedScalar(m[1], learnedStatus)
	if status == "" {
		status = "applied"
	}
	if base := learnedScalar(m[1], learnedBase); status != "applied" || base == "" || base != baseHash {
		return "", false
	}
	return jsTrim(m[2]), true
}

// appendLearnedGuidance is appendLearnedSection() in skill-overlay.ts.
func appendLearnedGuidance(skillPath, body string) error {
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return err
	}
	text := string(raw)
	if start := strings.Index(text, learnedStart); start >= 0 {
		end := strings.Index(text[start:], learnedEnd)
		if end < 0 {
			text = jsTrimEnd(text[:start])
		} else {
			text = jsTrimEnd(jsTrimEnd(text[:start]) + "\n" + jsTrimStart(text[start+end+len(learnedEnd):]))
		}
	} else {
		text = jsTrimEnd(text)
	}
	guidance := jsTrim(body)
	if guidance != "" {
		text = text + "\n\n" + learnedStart + "\n## Learned guidance\n\n" + guidance + "\n" + learnedEnd + "\n"
	}
	return os.WriteFile(skillPath, []byte(text), 0o644)
}

// shareStageWithGroup is shareWithGroup(): chmod -R g+rwX,o-rwx, so that
// serve, in the same group, can read and write the stage.
func shareStageWithGroup(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()&^0o007 | 0o060
		if d.IsDir() || info.Mode().Perm()&0o111 != 0 {
			mode |= 0o010
		}
		return os.Chmod(p, mode)
	})
}

// copyBack copies the run's artifacts to the host run folder, so the file
// route finds them. Links stay behind: the artifact scan skips them. The
// runner and its children are dead when it runs, but the runner chose
// every name in the tree.
func (s *turnStage) copyBack() error {
	src, dst := s.Workspace.ArtifactRoot, s.host.ArtifactRoot
	root, err := os.OpenRoot(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			seen, err := d.Info()
			if err != nil {
				return err
			}
			f, err := openStagedFile(root, rel, seen, sandbox.MaxTransfer)
			if err != nil {
				log.Printf("[agent-run] artifact %s skipped: %v", rel, err)
				return nil
			}
			defer f.Close()
			if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
				if err := os.RemoveAll(target); err != nil {
					return err
				}
			}
			if err := copyCapped(target, f, sandbox.MaxTransfer); err != nil {
				log.Printf("[agent-run] artifact %s skipped: %v", rel, err)
			}
			return nil
		}
		return nil
	})
}

// openStagedFile opens a file that the runner wrote, only when the opened
// file is the regular file the walk saw, has no other name, holds no hole
// and is at most limit bytes.
func openStagedFile(root *os.Root, rel string, seen fs.FileInfo, limit int64) (*os.File, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), stagedOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		err = stagedFileOK(f, info, seen, limit)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func stagedFileOK(f *os.File, info, seen fs.FileInfo, limit int64) error {
	switch {
	case !info.Mode().IsRegular() || (seen != nil && !os.SameFile(info, seen)):
		return errors.New("the file changed after the scan")
	case !singleLink(info):
		return errors.New("the file has another name outside the run")
	case info.Size() > limit:
		return fmt.Errorf("the file holds %d bytes, over the limit of %d MiB", info.Size(), limit>>20)
	case hasHole(f, info.Size()):
		return errors.New("the file is sparse")
	}
	return nil
}

// copyCapped writes at most limit bytes of src to target.
func copyCapped(target string, src io.Reader, limit int64) error {
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(src, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("the file grew over the limit of %d MiB", limit>>20)
	}
	if err != nil {
		_ = os.Remove(target)
	}
	return err
}

// readStagedFile reads a file that the runner wrote, as openStagedFile
// allows it.
func readStagedFile(root *os.Root, rel string, seen fs.FileInfo, limit int64) ([]byte, error) {
	f, err := openStagedFile(root, rel, seen, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("the file grew over the limit of %d MiB", limit>>20)
	}
	return data, err
}

// finish is the finally block of the launcher after the agent core: the
// artifacts come back unless the turn was cancelled, the skill edits are
// saved when the tool calls show the skills folder can have changed, and
// the stage goes.
func (s *turnStage) finish(ctx context.Context, a *API, orgID, runID string, aborted bool, evidence *skillEvidence) *savedSkillEdits {
	defer os.RemoveAll(s.Dir)
	if !s.Workspace.runners.settled() {
		log.Printf("[agent-run] run %s artifacts and skill edits skipped: serve did not report the runner's end", runID)
		return nil
	}
	if !aborted {
		if err := s.copyBack(); err != nil {
			log.Printf("[agent-run] artifact copy-back skipped: %v", err)
		}
	}
	if evidence != nil {
		evidence.Cwd = s.Workspace.OrgRoot
		evidence.SkillsRoots = []string{s.Workspace.SkillsRoot}
	}
	capture, reason := skillCaptureDecision(evidence)
	if s.captureReason != "" {
		capture, reason = true, s.captureReason
	}
	if !capture {
		log.Printf("[agent-run] run %s skill capture skipped: %s", runID, reason)
		return nil
	}
	saved, err := a.captureSkillEdits(ctx, orgID, runID, s.actor, s.baseline, s.staged, s.own, s.Workspace.SkillsRoot)
	if err != nil {
		log.Printf("[agent-run] skill capture failed: %v", err)
		return nil
	}
	if saved != nil {
		log.Printf("[agent-run] run %s saved skill edits (%s): proposed %s; refused %s; own %s; copies %s; skipped: %s", runID, reason,
			strings.Join(saved.Proposed, ", "), strings.Join(saved.Refused, ", "), strings.Join(saved.Own, ", "),
			strings.Join(saved.copyList(), ", "), strings.Join(saved.Skipped, ", "))
	}
	return saved
}

func jsTrimEnd(s string) string { return strings.TrimRightFunc(s, jsSpace) }
