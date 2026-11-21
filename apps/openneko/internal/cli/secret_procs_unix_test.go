//go:build !windows

package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/filestore"
	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/secretenv"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

const (
	secretRoleEnv   = "OPENNEKO_TEST_HANDOVER_ROLE"
	secretReportEnv = "OPENNEKO_TEST_HANDOVER_REPORT"
	plantedDBURL    = "postgres://u:planted-db-url-arg@db/openneko"
)

var plantedSecrets = map[string]string{
	"PLANTED_TOKEN":                        "planted-shell-token",
	"OPENAI_API_KEY":                       "planted-openai-key",
	"OPENNEKO_SESSION_SECRET":              "planted-session-secret",
	"NEKO_PG_PASSWORD":                     "planted-pg-password",
	"NEKO_EMBEDDING_API_KEY":               "planted-embedding-key",
	"OPENNEKO_SANDBOX_KEY":                 "planted-sandbox-key",
	"OPENNEKO_GITHUB_TOKEN":                "planted-github-token",
	"OPENNEKO_JOBS_WAKE_TOKEN":             "planted-wake-token",
	"OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN": "planted-audit-token",
	"GJ_DATABASE_PASSWORD":                 "planted-gj-password",
	"OTEL_EXPORTER_OTLP_HEADERS":           "authorization=planted-otel-header",
	"AWS_ACCESS_KEY_ID":                    "planted-aws-key-id",
	"AWS_SECRET_ACCESS_KEY":                "planted-aws-secret",
	"AWS_SESSION_TOKEN":                    "planted-aws-session",
}

// processBlock is what another process of the account reads of pid: its
// arguments and its environment block.
func processBlock(pid int) ([]byte, error) {
	if runtime.GOOS == "darwin" {
		return darwinProcargs(pid)
	}
	args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	return append(args, env...), err
}

func secretReport(role, line string) {
	f, err := os.OpenFile(os.Getenv(secretReportEnv), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %d %s\n", role, os.Getpid(), line)
}

func startRole(t *testing.T, role string, env []string, stdin []byte) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecretStandIn$", "-test.count=1")
	var kept []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, secretRoleEnv+"=") {
			kept = append(kept, kv)
		}
	}
	cmd.Env = append(kept, secretRoleEnv+"="+role, secretReportEnv+"="+os.Getenv(secretReportEnv))
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		secretReport(role, "start: "+err.Error())
		t.Fatal(err)
	}
}

// TestSecretStandIn runs one OpenNeko process of the hand-over chain:
// start, the supervisor, web, serve, the network part and the GraphJin
// child. Each keeps what it got and reports the secrets it can use.
func TestSecretStandIn(t *testing.T) {
	role := os.Getenv(secretRoleEnv)
	if role == "" {
		t.Skip("helper process")
	}
	layout := native.Layout{Root: os.Getenv("OPENNEKO_TEST_HANDOVER_ROOT")}
	exe := os.Args[0]
	switch role {
	case "start":
		supervisorArgs = func(exe string) []string {
			return []string{exe, "-test.run=^TestSecretStandIn$", "-test.count=1"}
		}
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, secretRoleEnv+"=") {
				env = append(env, kv)
			}
		}
		env = append(env, secretRoleEnv+"=supervisor")
		err := becomeSupervisor(layout, native.Options{Exe: exe, Env: env})
		secretReport(role, "exec: "+fmt.Sprint(err))
		return
	case "supervisor":
		env, err := handedSupervisorEnv(os.Stdin, os.Environ())
		if err != nil {
			secretReport(role, "error "+err.Error())
			return
		}
		secretReport(role, "ready")
		for _, p := range native.Procs(layout, native.Options{Exe: exe, Env: env}) {
			startRole(t, p.Name, p.Env, p.Stdin)
		}
	case "web":
		if err := secretenv.ReadHanded(os.Stdin); err != nil {
			secretReport(role, "error "+err.Error())
			return
		}
		for _, name := range []string{"OPENNEKO_SESSION_SECRET", "NEKO_PG_PASSWORD", "NEKO_EMBEDDING_API_KEY", "OPENNEKO_SANDBOX_KEY",
			"OPENNEKO_GITHUB_TOKEN", "OPENNEKO_JOBS_WAKE_TOKEN", "OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN"} {
			secretReport(role, "has "+name+"="+strconv.FormatBool(secretenv.Get(name) == plantedSecrets[name]))
		}
		secretReport(role, "ready")
	case "serve":
		if err := secretenv.ReadHanded(os.Stdin); err != nil {
			secretReport(role, "error "+err.Error())
			return
		}
		secretReport(role, "has OPENNEKO_SANDBOX_KEY="+strconv.FormatBool(secretenv.Get("OPENNEKO_SANDBOX_KEY") == plantedSecrets["OPENNEKO_SANDBOX_KEY"]))
		in := networkStart{FileStore: filestore.FromEnv(""), GraphJinSecrets: graphJinSecrets()}
		line, _ := json.Marshal(in)
		secretReport(role, "ready")
		startRole(t, "network", networkEnv(os.Environ()), append(line, '\n'))
	case "network":
		line, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
		var in networkStart
		if err == nil {
			err = json.Unmarshal(line, &in)
		}
		if err != nil {
			secretReport(role, "error "+err.Error())
			return
		}
		aws := in.FileStore.AWS
		secretReport(role, "has AWS="+strconv.FormatBool(aws.KeyID == plantedSecrets["AWS_ACCESS_KEY_ID"] &&
			aws.Secret == plantedSecrets["AWS_SECRET_ACCESS_KEY"] && aws.Session == plantedSecrets["AWS_SESSION_TOKEN"]))
		child := gjhost.SelfCommand("agentic.yml")("gj.sock")
		secretReport(role, "ready")
		startRole(t, "graphjin", child.Env, secretenv.Line(in.GraphJinSecrets))
	case "graphjin":
		if err := setHandedEnv(bufio.NewReader(os.Stdin)); err != nil {
			secretReport(role, "error "+err.Error())
			return
		}
		for _, name := range []string{"GJ_DATABASE_PASSWORD", "OTEL_EXPORTER_OTLP_HEADERS"} {
			secretReport(role, "has "+name+"="+strconv.FormatBool(os.Getenv(name) == plantedSecrets[name]))
		}
		secretReport(role, "ready")
	case "hold":
	}
	time.Sleep(10 * time.Minute)
}

// TestNoOpenNekoProcessShowsAPlantedSecret plants secrets in the shell of
// `openneko start` and the database URL in its arguments. Each process of
// the chain must still get the secrets it uses, and the arguments and
// environment that the OS shows of each must hold none of them. The read
// runs with no confinement, so it sees all that any process of the
// account can see.
func TestNoOpenNekoProcessShowsAPlantedSecret(t *testing.T) {
	root := testsock.Dir(t)
	layout := native.Layout{Root: root}
	if err := os.MkdirAll(layout.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(root, "report")
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "OPENNEKO_TEST_HANDOVER_ROOT=" + root,
		secretRoleEnv + "=start", secretReportEnv + "=" + report}
	for k, v := range plantedSecrets {
		env = append(env, k+"="+v)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecretStandIn$", "-test.count=1", "--", "start", "--database-url="+plantedDBURL)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })

	roles := []string{"supervisor", "web", "serve", "network", "graphjin"}
	pids := map[string]int{}
	var lines []string
	deadline := time.Now().Add(30 * time.Second)
	for len(pids) < len(roles) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		raw, _ := os.ReadFile(report)
		lines = strings.Split(strings.TrimSpace(string(raw)), "\n")
		for _, l := range lines {
			if f := strings.Fields(l); len(f) == 3 && f[2] == "ready" {
				pids[f[0]], _ = strconv.Atoi(f[1])
			}
		}
	}
	if len(pids) < len(roles) {
		t.Fatalf("the chain did not start: %v\n%s", pids, strings.Join(lines, "\n"))
	}
	if pids["supervisor"] != cmd.Process.Pid {
		t.Errorf("the supervisor runs as pid %d, not as the start process %d: start did not replace itself", pids["supervisor"], cmd.Process.Pid)
	}
	for _, l := range lines {
		if strings.Contains(l, " has ") && !strings.HasSuffix(l, "=true") || strings.Contains(l, " error ") {
			t.Errorf("a process lost a secret it uses: %s", l)
		}
	}
	planted := append([]string{plantedDBURL}, mapValues(plantedSecrets)...)
	for _, role := range roles {
		block, err := processBlock(pids[role])
		if err != nil {
			t.Errorf("read %s (pid %d): %v", role, pids[role], err)
			continue
		}
		for _, v := range planted {
			if bytes.Contains(block, []byte(v)) {
				t.Errorf("%s (pid %d) shows %q in its arguments or environment", role, pids[role], v)
			}
		}
	}

	control := exec.Command(os.Args[0], "-test.run=^TestSecretStandIn$", "-test.count=1", "--", "start", "--database-url="+plantedDBURL)
	control.Env = append(env[:3:3], secretRoleEnv+"=hold", secretReportEnv+"="+filepath.Join(root, "control"), "PLANTED_TOKEN="+plantedSecrets["PLANTED_TOKEN"])
	control.Stdin = strings.NewReader("")
	if err := control.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Process.Kill(); _ = control.Wait() })
	time.Sleep(300 * time.Millisecond)
	if block, err := processBlock(control.Process.Pid); err != nil || !bytes.Contains(block, []byte(plantedDBURL)) || !bytes.Contains(block, []byte(plantedSecrets["PLANTED_TOKEN"])) {
		t.Errorf("control: the read did not show the secrets of a process that holds them (%v)", err)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
