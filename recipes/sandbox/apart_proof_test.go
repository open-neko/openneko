package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var sharedPlaces = []string{"/tmp", "/var/tmp", "/run/lock", "/var/lock", "/dev/shm", "/dev/mqueue"}

const ipcTool = `import ctypes, os, sys
libc = ctypes.CDLL(None, use_errno=True)
libc.shmat.restype = ctypes.c_void_p
def make(key):
    i = libc.shmget(key, 4096, 0o1000 | 0o600)
    if i < 0:
        print("shm make: refused", os.strerror(ctypes.get_errno())); return
    p = libc.shmat(i, None, 0)
    ctypes.memmove(p, b"secret of run a", 15)
    print("shm make: made", i)
def attach():
    rows = open("/proc/sysvipc/shm").read().split("\n")[1:]
    ids = [r.split()[1] for r in rows if r.strip()]
    print("shm objects:", len(ids))
    for i in ids:
        p = libc.shmat(int(i), None, 0o10000)
        print("shm attach", i + ":", "refused" if p in (None, ctypes.c_void_p(-1).value) else "attached")
{"make": lambda: make(int(sys.argv[2])), "attach": attach}[sys.argv[1]]()
`

func placesScript(action string) string {
	return "for p in " + strings.Join(sharedPlaces, " ") + "; do\n" + action + "\ndone\n"
}

// writableOutside lists each folder and file the command's user can write,
// other than its run folder and home. In rootless Docker, find takes the
// device nodes in /dev for files, so the stat test drops them.
const writableOutside = `find / \( -path /proc -o -path /sys -o -path "$PWD" -o -path "$HOME" \) -prune -o \( -type d -o -type f \) -writable -print 2>/dev/null | while read -r p; do [ -c "$p" ] || echo "writable: $p"; done
`

func proveRunsApart(t *testing.T, image, bin string) {
	s, dir := newProofStack(t, "apart")
	s.files = []string{"compose.native.yml", writeFile(t, filepath.Join(dir, "proof.yml"), nativeOverlay(image, bin, ""))}
	s.up(t, dir)
	sandbox := s.container("openneko-sandbox")
	ipc := fileData{Path: "ipc.py", Mode: 0o644, Data: []byte(ipcTool)}

	t.Run("no shared place to write", func(t *testing.T) {
		run := s.client()
		defer run.Close()
		answer, err := run.send(context.Background(), execBody{Command: "bash cmd.sh", TimeoutSeconds: 120, Put: []fileData{{Path: "cmd.sh", Mode: 0o644, Data: []byte(writableOutside +
			placesScript(`if echo "secret of run a" > "$p/apart-a" 2>/dev/null || mkdir "$p/apart-a-dir" 2>/dev/null; then echo "$p: written"; else echo "$p: refused"; fi`) +
			"for d in /var/lib/openneko/sandbox-runs /var/lib/openneko/sandbox-homes; do if ls \"$d\" >/dev/null 2>&1; then echo \"$d: listed\"; else echo \"$d: refused\"; fi; done\n")}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(answer.Output, "\n") {
			if strings.HasPrefix(line, "writable: ") || strings.HasSuffix(line, ": written") || strings.HasSuffix(line, ": listed") {
				t.Errorf("a run user can write a shared place: %s", line)
			}
		}
		for _, p := range append(sharedPlaces, "/var/lib/openneko/sandbox-runs", "/var/lib/openneko/sandbox-homes") {
			if !strings.Contains(answer.Output, p+": refused\n") {
				t.Errorf("want %q in:\n%s", p+": refused", answer.Output)
			}
		}
	})

	t.Run("another run sees nothing, at the same time and after", func(t *testing.T) {
		a := s.client()
		key := "4242"
		done := make(chan execAnswer, 1)
		go func() {
			answer, err := a.send(context.Background(), execBody{Command: "bash cmd.sh", TimeoutSeconds: 120, Put: []fileData{ipc, {Path: "cmd.sh", Mode: 0o644, Data: []byte(
				placesScript(`echo "secret of run a" > "$p/apart-a" 2>/dev/null; mkdir "$p/apart-a-dir" 2>/dev/null`) +
					"python3 ipc.py make " + key + "\nid -u > uid\nsleep 6\n")}}})
			if err != nil {
				t.Error(err)
			}
			done <- answer
		}()
		time.Sleep(3 * time.Second)
		look := placesScript(`if ls -A "$p" 2>/dev/null | grep -q .; then echo "$p: listed"; else echo "$p: nothing"; fi
cat "$p/apart-a" 2>/dev/null && echo "$p: read"`) + "python3 ipc.py attach\n"
		signal := `for d in /proc/[0-9]*; do u=$(stat -c %u "$d" 2>/dev/null); if [ -n "$u" ] && [ "$u" != 0 ] && [ "$u" != "$(id -u)" ]; then if kill -0 "${d#/proc/}" 2>/dev/null; then echo "signal ${d#/proc/}: sent"; else echo "signal ${d#/proc/}: refused"; fi; fi; done
`
		b := s.client()
		during, err := b.send(context.Background(), execBody{Command: "bash cmd.sh", TimeoutSeconds: 60, Put: []fileData{ipc, {Path: "cmd.sh", Mode: 0o644, Data: []byte(look + signal)}}})
		if err != nil {
			t.Fatal(err)
		}
		_ = b.Close()
		first := <-done
		if !strings.Contains(first.Output, "shm make: made") {
			t.Fatalf("run a made no shared memory:\n%s", first.Output)
		}
		checkNothing(t, "during", during.Output)
		if !strings.Contains(during.Output, "signal ") {
			t.Errorf("run b found no process of run a:\n%s", during.Output)
		}
		if strings.Contains(during.Output, ": sent") {
			t.Errorf("run b can signal a process of run a:\n%s", during.Output)
		}
		if !regexp.MustCompile(`shm attach \d+: refused`).MatchString(during.Output) {
			t.Errorf("run b did not see run a's shared memory refused:\n%s", during.Output)
		}

		var uidA string
		for _, f := range first.Changed {
			if f.Path == "uid" {
				uidA = strings.TrimSpace(string(f.Data))
			}
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		c := s.client()
		defer c.Close()
		after, err := c.send(context.Background(), execBody{Command: "bash cmd.sh", TimeoutSeconds: 60, Put: []fileData{ipc, {Path: "cmd.sh", Mode: 0o644, Data: []byte(look +
			"echo \"uid $(id -u)\"\nfind / \\( -path /proc -o -path /sys -o -path \"$PWD\" -o -path \"$HOME\" \\) -prune -o -uid " + uidA + " -print 2>/dev/null | sed 's/^/left: /'\n")}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("run a had uid %s; the run after it: %s", uidA, regexp.MustCompile(`uid \d+`).FindString(after.Output))
		checkNothing(t, "after", after.Output)
		if !strings.Contains(after.Output, "shm objects: 0\n") {
			t.Errorf("run a's shared memory outlived it:\n%s", after.Output)
		}
		if strings.Contains(after.Output, "left: ") {
			t.Errorf("run a left files behind:\n%s", after.Output)
		}
	})

	t.Run("a tool that writes to /tmp fails and leaves nothing", func(t *testing.T) {
		run := s.client()
		defer run.Close()
		out := execIn(t, run, nil, `python3 -c "open('/tmp/report.csv', 'w').write('secret')"
echo "exit $?"
node -e "require('fs').writeFileSync('/var/tmp/report.csv', 'secret')" 2>&1 | grep -o "EACCES: permission denied, open '/var/tmp/report.csv'"
`)
		for _, want := range []string{"PermissionError: [Errno 13] Permission denied: '/tmp/report.csv'", "exit 1\n", "EACCES: permission denied, open '/var/tmp/report.csv'"} {
			if !strings.Contains(out, want) {
				t.Errorf("want %q in:\n%s", want, out)
			}
		}
		if left := inspectExec(t, sandbox, "find", "/tmp", "/var/tmp", "/run/lock", "/dev/shm", "/dev/mqueue", "-mindepth", "1"); strings.TrimSpace(left) != "" {
			t.Errorf("the shared places hold files:\n%s", left)
		}
	})

	t.Run("bundled skills still run", func(t *testing.T) { proveSkillsRun(t, s.client()) })
}

func checkNothing(t *testing.T, when, out string) {
	t.Helper()
	for _, p := range sharedPlaces {
		if !strings.Contains(out, p+": nothing\n") || strings.Contains(out, p+": read") {
			t.Errorf("%s: another run can list or read %s:\n%s", when, p, out)
		}
	}
}

func skillFiles(t *testing.T, names ...string) []fileData {
	t.Helper()
	var files []fileData
	for _, name := range names {
		err := filepath.WalkDir(filepath.Join("skills", name), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			info, _ := d.Info()
			files = append(files, fileData{Path: filepath.ToSlash(p), Mode: uint32(info.Mode().Perm()), Data: data})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func proveSkillsRun(t *testing.T, run *testClient) {
	defer run.Close()
	put := append(skillFiles(t, "xlsx", "docx", "pptx", "pdf", "skill-creator"),
		fileData{Path: "xlsx.json", Mode: 0o644, Data: []byte(`{"sheets":[{"name":"S","rows":[["Days",8],["Twice","=B1*2"]]}]}`)},
		fileData{Path: "docx.json", Mode: 0o644, Data: []byte(`{"blocks":[{"heading":"Summary","level":1},"Control-module ETA moved from 21 Aug to 29 Aug."]}`)},
		fileData{Path: "pptx.json", Mode: 0o644, Data: []byte(`{"slides":[{"title":"Q3 supplier review","subtitle":"Operations"}]}`)},
		fileData{Path: "pdf.json", Mode: 0o644, Data: []byte(`{"blocks":[{"heading":"Invoice 4471","level":1},"Total 18400"]}`)},
		fileData{Path: "cmd.sh", Mode: 0o644, Data: []byte(`set -e
S=skills
node $S/xlsx/scripts/xlsx.cjs create xlsx.json out.xlsx >/dev/null
node $S/xlsx/scripts/xlsx.cjs recalc out.xlsx >/dev/null
node $S/xlsx/scripts/xlsx.cjs read out.xlsx --format csv
node $S/docx/scripts/docx.cjs create docx.json out.docx >/dev/null
node $S/docx/scripts/docx.cjs read out.docx --format text
node $S/pptx/scripts/pptx.cjs create pptx.json out.pptx >/dev/null
node $S/pptx/scripts/pptx.cjs read out.pptx --format text
node $S/pdf/scripts/pdf.cjs create pdf.json out.pdf >/dev/null
node $S/pdf/scripts/pdf.cjs ocr out.pdf
mkdir -p demo && printf -- '---\nname: demo\ndescription: Says hello.\n---\n# Demo\n' > demo/SKILL.md
python3 $S/skill-creator/scripts/quick_validate.py demo
for v in TMPDIR TEMP TMP; do eval "d=\${$v:-}"; if [ "$d" = "$HOME/tmp" ]; then echo "$v: private"; else echo "$v: '$d'"; fi; done
echo "skills: done"
`)})
	answer, err := run.send(context.Background(), execBody{Command: "bash cmd.sh", TimeoutSeconds: 300, Put: put})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Twice,16", "Control-module ETA moved from 21 Aug to 29 Aug.", "Q3 supplier review", "Invoice 4471", "Skill is valid!", "TMPDIR: private", "TEMP: private", "TMP: private", "skills: done"} {
		if !strings.Contains(answer.Output, want) {
			t.Errorf("want %q in:\n%s", want, answer.Output)
		}
	}
}
