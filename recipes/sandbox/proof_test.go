//go:build linux

package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestEgressProof runs the recipe's compose files with real containers and
// checks the egress controls from inside a run. It needs Docker and
// OPENNEKO_EGRESS_PROOF_IMAGE (the sandbox image). The test builds the
// sandbox program from this folder and mounts it into the image, so a change
// here is tested without a new image.
func TestEgressProof(t *testing.T) {
	image := os.Getenv("OPENNEKO_EGRESS_PROOF_IMAGE")
	if image == "" && os.Getenv("OPENNEKO_EGRESS_PROOF_REQUIRED") == "1" {
		t.Fatal("OPENNEKO_EGRESS_PROOF_REQUIRED is set and OPENNEKO_EGRESS_PROOF_IMAGE is not: the release path must run the egress proof")
	}
	if image == "" {
		t.Skip("set OPENNEKO_EGRESS_PROOF_IMAGE to run the egress proof against real containers")
	}
	bin := buildProofBinary(t)
	t.Run("stack", func(t *testing.T) { proveStack(t, image, bin) })
	t.Run("native", func(t *testing.T) { proveNative(t, image, bin) })
	t.Run("firewall alone", func(t *testing.T) { proveFirewallAlone(t, image, bin) })
	t.Run("no firewall, no start", func(t *testing.T) { proveNoStartWithoutTheFirewall(t, image, bin) })
	t.Run("runs apart", func(t *testing.T) { proveRunsApart(t, image, bin) })
}

const runProxy = "127.0.0.1:3128"

type proofStack struct {
	project string
	tag     string
	files   []string
	env     []string
	socket  string
	key     string
}

func buildProofBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "openneko-sandbox")
	cmd := exec.Command("go", "build", "-trimpath", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func (s *proofStack) compose(t *testing.T, args ...string) string {
	t.Helper()
	out, err := s.composeErr(args...)
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (s *proofStack) composeErr(args ...string) (string, error) {
	full := []string{"compose", "-p", s.project}
	for _, f := range s.files {
		full = append(full, "-f", f)
	}
	cmd := exec.Command("docker", append(full, args...)...)
	cmd.Env = append(os.Environ(), s.env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// nameEverything gives each container, network and volume of the project a
// name that ends in the proof suffix, so the proof's leftovers are easy to find.
func (s *proofStack) nameEverything(t *testing.T, dir string) {
	t.Helper()
	var cfg struct {
		Services map[string]any `json:"services"`
		Networks map[string]any `json:"networks"`
		Volumes  map[string]any `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(s.compose(t, "--profile", "proof", "config", "--format", "json")), &cfg); err != nil {
		t.Fatal(err)
	}
	overlay := map[string]any{"services": map[string]any{}, "networks": map[string]any{}, "volumes": map[string]any{}}
	for name := range cfg.Services {
		overlay["services"].(map[string]any)[name] = map[string]any{"container_name": s.named(name)}
	}
	for name := range cfg.Networks {
		overlay["networks"].(map[string]any)[name] = map[string]any{"name": s.named(name)}
	}
	for name := range cfg.Volumes {
		overlay["volumes"].(map[string]any)[name] = map[string]any{"name": s.named(name)}
	}
	raw, _ := json.Marshal(overlay)
	path := filepath.Join(dir, "names.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	s.files = append(s.files, path)
}

func (s *proofStack) up(t *testing.T, dir string, services ...string) {
	t.Helper()
	s.upWant(t, dir, "closed", services...)
}

func (s *proofStack) upWant(t *testing.T, dir, egress string, services ...string) {
	t.Helper()
	s.nameEverything(t, dir)
	t.Cleanup(func() {
		if out, err := s.composeErr("--profile", "proof", "down", "-v", "--remove-orphans", "-t", "1"); err != nil {
			t.Logf("down: %v\n%s", err, out)
		}
	})
	s.compose(t, append([]string{"up", "-d", "--no-build", "--wait", "--wait-timeout", "120"}, services...)...)
	client := s.client()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		resp, err := client.do(ctx, "GET", "/v2/check", nil)
		cancel()
		if err == nil {
			var check struct{ Status, Egress string }
			_ = json.NewDecoder(resp.Body).Decode(&check)
			resp.Body.Close()
			if check.Egress != egress {
				t.Errorf("the sandbox reports egress %q, want %s", check.Egress, egress)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sandbox socket did not answer: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *proofStack) client() *testClient {
	c := newClient(s.socket)
	c.key = s.key
	return c
}

// proofSuffix ends every name of the proof, so that two people who run it
// on one machine do not share containers.
func proofSuffix() string {
	return cmp.Or(os.Getenv("OPENNEKO_EGRESS_PROOF_SUFFIX"), "e")
}

func (s *proofStack) named(name string) string {
	return name + "-" + s.tag + "-" + proofSuffix()
}

func (s *proofStack) container(service string) string {
	return s.named(service)
}

func inspect(t *testing.T, kind, format, name string) string {
	t.Helper()
	out, err := exec.Command("docker", kind, "inspect", "-f", format, name).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect %s: %v\n%s", name, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newProofStack(t *testing.T, tag string) (*proofStack, string) {
	dir := shortSocketDir(t)
	socketDir := filepath.Join(dir, "socket")
	if err := os.Mkdir(socketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &proofStack{project: "openneko-egress-" + tag + "-" + proofSuffix(), tag: tag, socket: filepath.Join(socketDir, "sandbox.sock"), key: newEgressToken()}
	s.env = []string{"OPENNEKO_VERSION=proof", "OPENNEKO_SANDBOX_KEY=" + s.key, "OPENNEKO_SANDBOX_DIR=" + socketDir,
		"OPENNEKO_SANDBOX_GID=" + strconv.Itoa(os.Getgid()), "OPENNEKO_DATABASE_URL=postgres://neko:secret@neko-db:5432/neko"}
	return s, dir
}

const proofListener = `import socket, sys, threading
def serve(port):
    s = socket.socket()
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", int(port)))
    s.listen()
    while True:
        c, _ = s.accept()
        c.sendall(b"HTTP/1.0 200 OK\r\n\r\nstack service\n")
        c.close()
for p in sys.argv[1:]:
    threading.Thread(target=serve, args=(p,)).start()
`

func sandboxImageYAML(image, bin string) string {
	return fmt.Sprintf(`    image: %q
    build: !reset null
    pull_policy: never
    volumes:
      - type: bind
        source: %q
        target: /usr/local/bin/openneko-sandbox
        read_only: true
`, image, bin)
}

// proofName resolves only through Docker's DNS on the proof's networks, and
// proofMetadata only in the proxy, to the metadata address. The proof then
// does not depend on the resolver of the cloud that runs it.
const (
	proofName     = "names.proof.test"
	proofMetadata = "metadata.proof.test"
)

func egressYAML(image, bin string) string {
	return sandboxImageYAML(image, bin) + fmt.Sprintf(`    extra_hosts: ["%s:169.254.169.254"]
    networks:
      openneko-sandbox:
        aliases: [%[2]s]
      openneko-egress:
        aliases: [%[2]s]
`, proofMetadata, proofName)
}

func controlYAML(image string, networks ...string) string {
	return fmt.Sprintf(`  proof-control:
    image: %q
    pull_policy: never
    profiles: ["proof"]
    entrypoint: ["bash"]
    networks: [%s]
`, image, strings.Join(networks, ", "))
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type target struct {
	label, host string
	port        int
	control     bool
}

// probeScript prints "<label> connected" or "<label> blocked" for each TCP
// target, and the same for each name lookup and for a direct DNS query.
func probeScript(targets []target, names []string) string {
	var b strings.Builder
	b.WriteString("probe() { if timeout 5 bash -c \"exec 3<>/dev/tcp/$2/$3\" 2>/dev/null; then echo \"$1 connected\"; else echo \"$1 blocked\"; fi; }\n")
	for _, tg := range targets {
		fmt.Fprintf(&b, "probe %q %q %d &\n", tg.label, tg.host, tg.port)
	}
	for _, n := range names {
		fmt.Fprintf(&b, "(if timeout 5 getent ahosts %q >/dev/null; then echo \"dns:%s connected\"; else echo \"dns:%s blocked\"; fi) &\n", n, n, n)
	}
	b.WriteString(`python3 - <<'PY' &
import socket
q = b"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01"
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(4)
try:
    s.sendto(q, ("8.8.8.8", 53))
    s.recv(512)
    print("udp-dns:8.8.8.8 connected")
except OSError:
    print("udp-dns:8.8.8.8 blocked")
PY
wait
`)
	return b.String()
}

func parseProbe(out string) map[string]string {
	got := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if label, result, ok := strings.Cut(strings.TrimSpace(sc.Text()), " "); ok && (result == "connected" || result == "blocked") {
			got[label] = result
		}
	}
	return got
}

// checkClosed runs the probe from an open control container first, so that
// each blocked result in the sandbox stands for a target that is there.
func checkClosed(t *testing.T, s *proofStack, run *testClient, targets []target, names []string) {
	t.Helper()
	script := probeScript(targets, names)
	control := parseProbe(s.compose(t, "--profile", "proof", "run", "--rm", "--no-deps", "proof-control", "-c", script))
	for _, tg := range targets {
		if tg.control && control[tg.label] != "connected" {
			t.Fatalf("control: %s (%s:%d) is %q from an open network, so the sandbox result proves nothing", tg.label, tg.host, tg.port, control[tg.label])
		}
	}
	for _, n := range append(names, "udp-dns:8.8.8.8") {
		key := n
		if !strings.HasPrefix(n, "udp-dns:") {
			key = "dns:" + n
		}
		if control[key] != "connected" {
			t.Fatalf("control: %s is %q from an open network", key, control[key])
		}
	}
	answer, err := run.send(context.Background(), execBody{Command: "bash probe.sh", TimeoutSeconds: 60,
		Put: []fileData{{Path: "probe.sh", Mode: 0o644, Data: []byte(script)}}, Egress: &egressBody{Org: "org-proof", Label: "closed"}})
	if err != nil {
		t.Fatal(err)
	}
	got := parseProbe(answer.Output)
	for _, tg := range targets {
		if got[tg.label] != "blocked" {
			t.Errorf("a command reached %s (%s:%d): %q", tg.label, tg.host, tg.port, got[tg.label])
		}
	}
	for _, n := range names {
		if got["dns:"+n] != "blocked" {
			t.Errorf("a command resolved %s: %q", n, got["dns:"+n])
		}
	}
	if got["udp-dns:8.8.8.8"] != "blocked" {
		t.Errorf("a command got a DNS answer from 8.8.8.8: %q", got["udp-dns:8.8.8.8"])
	}
	t.Logf("from the sandbox:\n%s", answer.Output)
}

func hostAddress(t *testing.T) string {
	if a := os.Getenv("OPENNEKO_EGRESS_PROOF_HOST"); a != "" {
		return a
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !strings.HasPrefix(n.IP.String(), "172.") {
			return n.IP.String()
		}
	}
	t.Fatal("no host address; set OPENNEKO_EGRESS_PROOF_HOST")
	return ""
}

func publicTargets(t *testing.T) []target {
	host := hostAddress(t)
	port := 22
	if p := os.Getenv("OPENNEKO_EGRESS_PROOF_HOST_PORT"); p != "" {
		port, _ = strconv.Atoi(p)
	}
	return []target{
		{"public:1.1.1.1:443", "1.1.1.1", 443, true},
		{"public:8.8.8.8:53", "8.8.8.8", 53, true},
		{fmt.Sprintf("docker-host:%s:%d", host, port), host, port, true},
		{"metadata:169.254.169.254:80", "169.254.169.254", 80, true},
	}
}

func gatewayPort() int {
	if p, err := strconv.Atoi(os.Getenv("OPENNEKO_EGRESS_PROOF_GATEWAY_PORT")); err == nil {
		return p
	}
	return 39187
}

// listenOnTheGateway starts a listener in the network namespace that holds
// Docker's bridges, so each bridge gateway has a port that answers. Rootful
// Docker keeps the bridges in the host's namespace; rootless Docker keeps
// them in the namespace of its RootlessKit child.
func listenOnTheGateway(t *testing.T, s *proofStack, image string) {
	t.Helper()
	port := strconv.Itoa(gatewayPort())
	state := filepath.Join(cmp.Or(os.Getenv("XDG_RUNTIME_DIR"), fmt.Sprintf("/run/user/%d", os.Getuid())), "dockerd-rootless")
	info, _ := exec.Command("docker", "info", "-f", "{{.SecurityOptions}}").Output()
	if !strings.Contains(string(info), "rootless") {
		name := s.named("gateway-listener")
		if out, err := exec.Command("docker", "run", "-d", "--name", name, "--network", "host", "--entrypoint", "python3", image, "-c", proofListener, port).CombinedOutput(); err != nil {
			t.Fatalf("gateway listener: %v\n%s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
		return
	}
	if p := os.Getenv("OPENNEKO_EGRESS_PROOF_HOST_PORT"); p != "" {
		listenOnTheHost(t, p)
	}
	pid, err := os.ReadFile(filepath.Join(state, "child_pid"))
	if err != nil {
		t.Fatalf("rootless Docker, and its RootlessKit state is not readable: %v", err)
	}
	args := []string{"-U", "--preserve-credentials", "-t", strings.TrimSpace(string(pid))}
	if _, err := os.Stat(filepath.Join(state, "netns")); err == nil {
		args = append(args, "-m", "--", "nsenter", "--net="+filepath.Join(state, "netns"))
	} else {
		args = append(args, "-n", "--")
	}
	cmd := exec.Command("nsenter", append(args, "python3", "-c", proofListener, port)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("gateway listener: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	time.Sleep(500 * time.Millisecond)
}

// listenOnTheHost answers on the host's own address. Rootful Docker gets
// this from the host-network gateway listener; rootless Docker does not.
func listenOnTheHost(t *testing.T, port string) {
	t.Helper()
	l, err := net.Listen("tcp", ":"+port)
	if err != nil {
		t.Fatalf("host listener: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.0 200 OK\r\n\r\nstack service\n"))
			_ = c.Close()
		}
	}()
}

func gatewayTarget(t *testing.T, network string) target {
	gw := inspect(t, "network", `{{range .IPAM.Config}}{{.Gateway}}{{end}}`, network)
	return target{"docker-host:sandbox-gateway", gw, gatewayPort(), true}
}

func nativeOverlay(image, bin, extra string) string {
	return "services:\n  openneko-sandbox:\n" + sandboxImageYAML(image, bin) + "  openneko-egress:\n" + egressYAML(image, bin) +
		controlYAML(image, "openneko-egress") + extra
}

func proveNative(t *testing.T, image, bin string) {
	s, dir := newProofStack(t, "native")
	s.files = []string{"compose.native.yml", writeFile(t, filepath.Join(dir, "proof.yml"), nativeOverlay(image, bin, ""))}
	s.up(t, dir)
	run := s.client()
	defer run.Close()
	listenOnTheGateway(t, s, image)
	targets := append(publicTargets(t), gatewayTarget(t, s.named("openneko-sandbox")))
	checkClosed(t, s, run, targets, []string{"example.com", proofName})

	status := inspectExec(t, s.container("openneko-sandbox"), "cat", "/proc/1/status")
	if err := capsWithout(strings.NewReader(status), capNetAdmin, capSetPCap); err != nil {
		t.Errorf("the sandbox process kept a capability after its start: %v", err)
	}
	if rules := inspectExec(t, s.container("openneko-sandbox"), "nft", "list", "table", "inet", firewallTable); !strings.Contains(rules, "drop") {
		t.Errorf("the firewall table is not in place:\n%s", rules)
	}
	out := execIn(t, run, nil, `python3 - <<'PY'
import socket
srv = socket.socket()
srv.bind(("127.0.0.1", 39190))
srv.listen()
print("own listener: bound")
c = socket.socket()
c.settimeout(3)
try:
    c.connect(("127.0.0.1", 39190))
    print("own loopback: connected")
except OSError:
    print("own loopback: blocked")
PY`)
	for _, want := range []string{"own listener: bound", "own loopback: blocked"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
}

func inspectExec(t *testing.T, container string, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", append([]string{"exec", container}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %s %v: %v\n%s", container, args, err, out)
	}
	return string(out)
}

// proveFirewallAlone puts the sandbox on a network with a route out: the
// sandbox must say so, and the firewall must still keep every run in.
func proveFirewallAlone(t *testing.T, image, bin string) {
	s, dir := newProofStack(t, "open")
	extra := "networks:\n  openneko-sandbox:\n    internal: false\n"
	s.files = []string{"compose.native.yml", writeFile(t, filepath.Join(dir, "proof.yml"), nativeOverlay(image, bin, extra))}
	s.upWant(t, dir, "open")
	listenOnTheGateway(t, s, image)
	run := s.client()
	defer run.Close()
	targets := append(publicTargets(t), gatewayTarget(t, s.named("openneko-sandbox")))
	checkClosed(t, s, run, targets, []string{"example.com", proofName})
}

// proveNoStartWithoutTheFirewall removes one capability that the firewall
// needs: the sandbox must exit, not serve.
func proveNoStartWithoutTheFirewall(t *testing.T, image, bin string) {
	for _, missing := range []string{"NET_ADMIN", "SETPCAP"} {
		t.Run(missing, func(t *testing.T) {
			tag := "no" + strings.ToLower(strings.ReplaceAll(missing, "_", ""))
			s, dir := newProofStack(t, tag)
			var caps []string
			for _, c := range []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "KILL", "SETGID", "SETUID", "NET_ADMIN", "SETPCAP"} {
				if c != missing {
					caps = append(caps, c)
				}
			}
			overlay := "services:\n  openneko-sandbox:\n" + sandboxImageYAML(image, bin) +
				"    restart: \"no\"\n    cap_add: !override [" + strings.Join(caps, ", ") + "]\n"
			s.files = []string{"compose.native.yml", writeFile(t, filepath.Join(dir, "proof.yml"), overlay)}
			s.nameEverything(t, dir)
			t.Cleanup(func() { _, _ = s.composeErr("down", "-v", "--remove-orphans", "-t", "1") })
			s.compose(t, "up", "-d", "--no-build", "--no-deps", "openneko-sandbox")
			name := s.container("openneko-sandbox")
			var state string
			for i := 0; i < 150; i++ {
				if state = inspect(t, "container", "{{.State.Status}} {{.State.ExitCode}}", name); strings.HasPrefix(state, "exited") {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			if state != "exited 1" || !strings.Contains(string(logs), "so the sandbox does not start") {
				t.Errorf("without %s: state %q, logs:\n%s", missing, state, logs)
			}
			if _, err := os.Stat(s.socket); err == nil {
				t.Errorf("without %s the sandbox opened its socket", missing)
			}
		})
	}
}

func proveStack(t *testing.T, image, bin string) {
	s, dir := newProofStack(t, "stack")
	db := os.Getenv("OPENNEKO_EGRESS_PROOF_DB_IMAGE")
	if db == "" {
		db = "pgvector/pgvector:0.8.6-pg16"
	}
	listener, _ := json.Marshal(proofListener)
	stand := func(ports string) string {
		return fmt.Sprintf("    image: %q\n    build: !reset null\n    pull_policy: never\n    entrypoint: [\"python3\", \"-c\", %s]\n    command: [%s]\n    ports: !reset []\n", image, listener, ports)
	}
	overlay := "services:\n" +
		fmt.Sprintf("  neko-db:\n    image: %q\n    build: !reset null\n    pull_policy: never\n    ports: !reset []\n", db) +
		"  openneko-serve:\n" + stand(`"8080", "8089", "8090"`) +
		"  web:\n" + stand(`"8080"`) +
		"  openneko-sandbox:\n" + strings.Replace(sandboxImageYAML(image, bin), "    volumes:\n",
		fmt.Sprintf("    volumes:\n      - type: bind\n        source: %q\n        target: /run/openneko-sandbox\n", filepath.Dir(s.socket)), 1) +
		"  openneko-egress:\n" + egressYAML(image, bin) +
		"  intranet:\n" + stand(`"8080"`) + "    networks: [openneko-egress]\n" +
		controlYAML(image, "default", "openneko-egress")
	s.files = []string{"../../compose.yml", "../../compose.db.yml", "compose.yml", writeFile(t, filepath.Join(dir, "proof.yml"), overlay)}
	s.up(t, dir, "neko-db", "openneko-serve", "web", "openneko-sandbox", "openneko-egress", "intranet")

	ipOf := func(service string) string {
		return inspect(t, "container", `{{range $k, $v := .NetworkSettings.Networks}}{{if eq $k "`+s.named("default")+`"}}{{$v.IPAddress}}{{end}}{{end}}`, s.container(service))
	}
	stack := []target{
		{"stack:neko-db", "neko-db", 5432, true},
		{"stack:neko-db-ip", ipOf("neko-db"), 5432, true},
		{"stack:web", "web", 8080, true},
		{"stack:web-ip", ipOf("web"), 8080, true},
		{"stack:openneko-serve", "openneko-serve", 8090, true},
		{"stack:graphjin", "graphjin", 8080, true},
		{"stack:openneko-serve-ip", ipOf("openneko-serve"), 8089, true},
		gatewayTarget(t, s.named("openneko-sandbox")),
	}

	t.Run("a: an empty list reaches nothing", func(t *testing.T) {
		listenOnTheGateway(t, s, image)
		run := s.client()
		defer run.Close()
		checkClosed(t, s, run, append(publicTargets(t), stack...), []string{"example.com", proofName, "web"})
		out := execIn(t, run, nil, `printf 'no token: '; fetch --proxy http://`+runProxy+` http://example.com/
printf 'listed none: '; fetch https://example.com/
printf 'stack by proxy: '; fetch http://web:8080/`)
		for _, want := range []string{"no token: status 407", "listed none: connect 403", "stack by proxy: status 403"} {
			if !strings.Contains(out, want+"\n") {
				t.Errorf("want %q in:\n%s", want, out)
			}
		}
	})

	t.Run("b: one listed host and no other", func(t *testing.T) {
		run := s.client()
		defer run.Close()
		hosts := []string{"example.com", "web:8080", "intranet:8080", proofMetadata + ":80", "169.254.169.254:80"}
		out := execIn(t, run, &egressBody{Org: "org-proof", Label: "work-run-b", Hosts: hosts}, `c() { printf '%s: ' "$1"; shift; fetch "$@"; }
c listed https://example.com/
c other-host https://www.iana.org/
c other-port http://example.com/
c stack-web http://web:8080/
c private-name http://intranet:8080/
c private-tunnel --tunnel intranet:8080
c metadata-name http://`+proofMetadata+`/
c metadata-ip http://169.254.169.254/
c no-proxy --direct https://example.com/`)
		for _, want := range []string{"listed: status 200", "other-host: connect 403", "other-port: status 403", "stack-web: status 403",
			"private-name: status 403", "private-tunnel: connect 403", "metadata-name: status 403", "metadata-ip: status 403", "no-proxy: error"} {
			if !strings.Contains(out, want+"\n") {
				t.Errorf("want %q in:\n%s", want, out)
			}
		}
		logs, _ := exec.Command("docker", "logs", s.container("openneko-egress")).CombinedOutput()
		var allowed, private int
		for _, line := range strings.Split(string(logs), "\n") {
			var e egressLog
			if json.Unmarshal([]byte(line), &e) != nil || e.Run != run.run {
				continue
			}
			if e.Org != "org-proof" || e.Label != "work-run-b" {
				t.Errorf("a log line lacks the run's org and label: %s", line)
			}
			if e.Result == "allowed" && e.Host == "example.com" && e.Port == 443 {
				allowed++
			}
			if e.Result == "refused" && e.Reason == errNotPublic.Error() {
				private++
			}
		}
		if allowed != 1 || private != 4 {
			t.Errorf("the proxy log holds %d allowed and %d not-public lines for the run, want 1 and 4 (intranet twice, metadata by name and by number):\n%s", allowed, private, logs)
		}
	})

	t.Run("c: a token belongs to its run and ends with it", func(t *testing.T) {
		a, b := s.client(), s.client()
		defer b.Close()
		listed := &egressBody{Org: "org-proof", Label: "run-a", Hosts: []string{"example.com"}}
		proxyA := strings.TrimSpace(execIn(t, a, listed, `printf %s "$HTTPS_PROXY"`))
		m := regexp.MustCompile(`^http://(run-[A-Za-z0-9]+):([0-9a-f]{64})@` + regexp.QuoteMeta(runProxy) + `$`).FindStringSubmatch(proxyA)
		if m == nil || m[1] != a.run {
			t.Fatalf("run a's proxy = %q", proxyA)
		}
		tokenA := m[2]
		execIn(t, b, &egressBody{Org: "org-proof", Label: "run-b"}, "true")

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = a.send(context.Background(), execBody{Command: "sleep 8", TimeoutSeconds: 20})
		}()
		time.Sleep(2 * time.Second)
		out := execIn(t, b, nil, fmt.Sprintf(`printf 'run a visible: '; for f in /proc/[0-9]*/cmdline; do tr '\0' ' ' < "$f"; echo; done 2>/dev/null | grep -q '^sleep 8 $' && echo yes || echo no
printf 'token read: '; cat /proc/[0-9]*/environ 2>/dev/null | tr '\0' '\n' | grep -c %s
printf 'key read: '; cat /proc/[0-9]*/environ 2>/dev/null | tr '\0' '\n' | grep -c %s
printf 'pid 1 environ: '; cat /proc/1/environ >/dev/null 2>&1 && echo readable || echo refused
printf 'b with a token: '; fetch --proxy http://%s:%s@`+runProxy+` https://example.com/`,
			tokenA, s.key, b.run, tokenA))
		wg.Wait()
		for _, want := range []string{"run a visible: yes", "token read: 0", "key read: 0", "pid 1 environ: refused", "b with a token: connect 407"} {
			if !strings.Contains(out, want+"\n") {
				t.Errorf("want %q in:\n%s", want, out)
			}
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		out = execIn(t, b, nil, fmt.Sprintf(`printf 'ended run token: '; fetch --proxy %s https://example.com/`, proxyA))
		if !strings.Contains(out, "ended run token: connect 407\n") {
			t.Errorf("the token of an ended run still worked:\n%s", out)
		}
	})

	t.Run("d: the bundled skills work with an empty list", func(t *testing.T) {
		run := s.client()
		defer run.Close()
		var put []fileData
		err := filepath.WalkDir("skills", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			if d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			put = append(put, fileData{Path: path, Mode: 0o644, Data: data})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		answer, err := run.send(context.Background(), execBody{TimeoutSeconds: 900, MaxOutput: 200_000, Put: put, Egress: &egressBody{Org: "org-proof", Label: "skills"},
			Command: `cd skills && for skill in xlsx docx pptx pdf; do node --test --test-reporter=tap "test/$skill.test.cjs" > "$skill.log" 2>&1; echo "$skill exit $? $(grep -E '^# (tests|fail) ' "$skill.log" | tr '\n' ' ')"; done
mkdir -p ../new-skill && printf -- '---\nname: new-skill\ndescription: Says hello.\n---\n\nSay hello.\n' > ../new-skill/SKILL.md
cd skill-creator && python3 -m scripts.quick_validate ../../new-skill && python3 -m scripts.package_skill ../../new-skill ../../dist >/dev/null; echo "skill-creator exit $?"; ls ../../dist`})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("skills:\n%s", answer.Output)
		for _, skill := range []string{"xlsx", "docx", "pptx", "pdf"} {
			m := regexp.MustCompile(`(?m)^` + skill + ` exit (\d+) # tests (\d+) # fail (\d+) $`).FindStringSubmatch(answer.Output)
			if m == nil || m[1] != "0" || m[2] == "0" || m[3] != "0" {
				t.Errorf("the %s skill tests did not pass: %v", skill, m)
			}
		}
		for _, want := range []string{"Skill is valid!", "skill-creator exit 0", "new-skill.skill"} {
			if !strings.Contains(answer.Output, want) {
				t.Errorf("want %q in:\n%s", want, answer.Output)
			}
		}
	})
}

// proofFetch is what a command in the image has for HTTP: Python's urllib,
// which reads the proxy variables. It prints "status <code>", "connect
// <code>" for a refused tunnel, or "error".
const proofFetch = `import base64, os, re, socket, sys, urllib.error, urllib.request
from urllib.parse import urlsplit
args = sys.argv[1:]
proxy, mode = None, "env"
while args[0].startswith("--"):
    flag = args.pop(0)
    if flag == "--proxy":
        proxy, mode = args.pop(0), "proxy"
    else:
        mode = flag[2:]
url = args[0]
try:
    if mode == "tunnel":
        p = urlsplit(os.environ["HTTPS_PROXY"])
        s = socket.create_connection((p.hostname, p.port), timeout=20)
        auth = base64.b64encode(f"{p.username}:{p.password}".encode()).decode()
        s.sendall(f"CONNECT {url} HTTP/1.1\r\nHost: {url}\r\nProxy-Authorization: Basic {auth}\r\n\r\n".encode())
        print("connect", s.recv(64).split(b" ")[1].decode())
        sys.exit()
    handler = {"env": urllib.request.ProxyHandler(), "direct": urllib.request.ProxyHandler({}),
               "proxy": urllib.request.ProxyHandler({"http": proxy, "https": proxy})}[mode]
    print("status", urllib.request.build_opener(handler).open(url, timeout=20).status)
except urllib.error.HTTPError as e:
    print("status", e.code)
except OSError as e:
    m = re.search(r"Tunnel connection failed: (\d+)", str(e))
    print("connect " + m.group(1) if m else "error")
`

func execIn(t *testing.T, run *testClient, egress *egressBody, script string) string {
	t.Helper()
	answer, err := run.send(context.Background(), execBody{Command: "bash cmd.sh", TimeoutSeconds: 120,
		Put: []fileData{{Path: "cmd.sh", Mode: 0o644, Data: []byte("fetch() { python3 fetch.py \"$@\"; }\n" + script)},
			{Path: "fetch.py", Mode: 0o644, Data: []byte(proofFetch)}}, Egress: egress})
	if err != nil {
		t.Fatal(err)
	}
	return answer.Output
}
