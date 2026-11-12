package webfront

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const skillOriginFile = ".openneko-origin"

var (
	sandboxSkills       = map[string]bool{"docx": true, "pdf": true, "pptx": true, "skill-creator": true, "xlsx": true}
	retiredBuiltinSkill = []string{"3-statement-model", "comps-analysis", "dcf-model", "document-extraction", "docx",
		"excel-author", "internal-comms", "lbo-model", "merger-model", "pdf", "pptx", "pptx-author", "xlsx"}
	knowledgeFiles = [][2]string{
		{"INDEX.md", "# OpenNeko Knowledge\n\nThis directory is reserved for org-specific durable knowledge files.\nUse these paths when you need reusable facts beyond the active chat.\n\n- schema.json\n- insights.json\n- syntax.json"},
		{"schema.json", "{}\n"}, {"insights.json", "{}\n"}, {"syntax.json", "{}\n"},
	}
	workspaceMu sync.Mutex
	collatorMu  sync.Mutex
	collator    = collate.New(language.Und)
)

// localeCompare orders strings as String.prototype.localeCompare does in Node.
func localeCompare(a, b string) int {
	collatorMu.Lock()
	defer collatorMu.Unlock()
	return collator.CompareString(a, b)
}

type orgRoots struct {
	Org, Skills, Memory, Knowledge, Uploads, Runs string
}

// orgWorkspace is ensureOrgWorkspace() in packages/llm/src/work/workspace.ts.
func orgWorkspace(orgID string) (orgRoots, error) {
	root := orgAgentRoot(orgID)
	r := orgRoots{Org: root, Skills: filepath.Join(root, "skills"), Memory: filepath.Join(root, "memory"),
		Knowledge: filepath.Join(root, "knowledge"), Uploads: filepath.Join(root, "uploads"), Runs: filepath.Join(root, "runs")}
	for _, dir := range []string{r.Org, r.Skills, r.Memory, r.Knowledge, r.Uploads, r.Runs, filepath.Join(root, "skill-overlays")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return r, err
		}
	}
	workspaceMu.Lock()
	defer workspaceMu.Unlock()
	if err := removeRetiredBuiltins(r.Skills); err != nil {
		return r, err
	}
	if err := seedBuiltinSkills(r.Skills); err != nil {
		return r, err
	}
	for _, f := range knowledgeFiles {
		path := filepath.Join(r.Knowledge, f[0])
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(f[1]), 0o644); err != nil {
			return r, err
		}
	}
	return r, nil
}

func assetsRoot(env, sub string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		abs, _ := filepath.Abs(v)
		return abs
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		for _, candidate := range []string{filepath.Join(dir, "packages", "llm", "assets", sub), filepath.Join(dir, "assets", sub)} {
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func builtinSkillsRoot() string { return assetsRoot("OPENNEKO_BUILTIN_SKILLS_ROOT", "builtin-skills") }
func sandboxSkillsRoot() string { return assetsRoot("OPENNEKO_SANDBOX_SKILLS_ROOT", "sandbox-skills") }

func bundledSkillDir(name string) string {
	if root := sandboxSkillsRoot(); root != "" {
		if _, err := os.Stat(filepath.Join(root, name, "SKILL.md")); err == nil {
			return filepath.Join(root, name)
		}
	}
	if root := builtinSkillsRoot(); root != "" {
		return filepath.Join(root, name)
	}
	return ""
}

func bundledSkillNames() []string {
	names := map[string]bool{}
	if root := builtinSkillsRoot(); root != "" {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.IsDir() {
				names[e.Name()] = true
			}
		}
	}
	if root := sandboxSkillsRoot(); root != "" {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(root, e.Name(), "SKILL.md")); e.IsDir() && err == nil {
				names[e.Name()] = true
			}
		}
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func skillRunnable(name string) bool {
	return !sandboxSkills[name] || strings.TrimSpace(os.Getenv("OPENNEKO_SANDBOX_URL")) != ""
}

type skillOrigin struct {
	Kind       string `json:"kind"`
	SourceHash string `json:"sourceHash"`
}

func readSkillOrigin(dir string) *skillOrigin {
	raw, err := os.ReadFile(filepath.Join(dir, skillOriginFile))
	if err != nil {
		return nil
	}
	var o skillOrigin
	if json.Unmarshal(raw, &o) != nil || o.Kind != "builtin" || o.SourceHash == "" {
		return nil
	}
	return &o
}

// fingerprintSkillTree is fingerprintSkillTree() in workspace.ts; the hash
// is stored in the origin file, so the walk order must match Node's.
func fingerprintSkillTree(root string) (string, error) {
	h := sha256.New()
	var visit func(dir, prefix string) error
	visit = func(dir, prefix string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return localeCompare(a.Name(), b.Name()) })
		for _, e := range entries {
			if prefix == "" && e.Name() == skillOriginFile {
				continue
			}
			rel := e.Name()
			if prefix != "" {
				rel = prefix + "/" + e.Name()
			}
			abs := filepath.Join(dir, e.Name())
			switch {
			case e.IsDir():
				io.WriteString(h, "d:"+rel+"\x00")
				if err := visit(abs, rel); err != nil {
					return err
				}
			case e.Type()&fs.ModeSymlink != 0:
				target, err := os.Readlink(abs)
				if err != nil {
					return err
				}
				io.WriteString(h, "l:"+rel+"\x00"+target+"\x00")
			case e.Type().IsRegular():
				data, err := os.ReadFile(abs)
				if err != nil {
					return err
				}
				io.WriteString(h, "f:"+rel+"\x00")
				h.Write(data)
				io.WriteString(h, "\x00")
			}
		}
		return nil
	}
	if err := visit(root, ""); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func removeRetiredBuiltins(skillsRoot string) error {
	for _, name := range retiredBuiltinSkill {
		dir := filepath.Join(skillsRoot, name)
		origin := readSkillOrigin(dir)
		if origin == nil {
			continue
		}
		if hash, _ := fingerprintSkillTree(dir); hash != origin.SourceHash {
			continue
		}
		if bundled := bundledSkillDir(name); bundled != "" {
			if _, err := os.Stat(bundled); err == nil {
				if hash, _ := fingerprintSkillTree(bundled); hash == origin.SourceHash {
					continue
				}
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func seedBuiltinSkills(skillsRoot string) error {
	for _, name := range bundledSkillNames() {
		if !skillRunnable(name) {
			continue
		}
		dest := filepath.Join(skillsRoot, name)
		if _, err := os.Lstat(dest); err == nil {
			continue
		}
		source := bundledSkillDir(name)
		if err := copyTree(source, dest); err != nil {
			return err
		}
		hash, err := fingerprintSkillTree(source)
		if err != nil {
			return err
		}
		origin, _ := json.Marshal(skillOrigin{Kind: "builtin", SourceHash: hash})
		if err := os.WriteFile(filepath.Join(dest, skillOriginFile), append(origin, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		}
		return nil
	})
}

func isWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)))
}
