package webfront

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// generateDeployKey is generateDeployKey() in config-vcs/ssh.ts: an ed25519
// key pair in OpenSSH format.
func generateDeployKey(comment string) (privateKey, publicKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment, nil
}

var errHostKeySeen = errors.New("host key read")

var hostKeyAlgorithmSets = [][]string{
	{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA},
	{ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521},
	{ssh.KeyAlgoED25519},
}

// scanHostKeys is scanHostKeys() in config-vcs/ssh.ts: the server's host
// keys in known_hosts format, as ssh-keyscan prints them.
func scanHostKeys(host string, port int) (string, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	var lines []string
	for _, algorithms := range hostKeyAlgorithmSets {
		conn, err := net.DialTimeout("tcp", address, 10*time.Second)
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		var key ssh.PublicKey
		config := &ssh.ClientConfig{
			User:              "openneko",
			HostKeyAlgorithms: algorithms,
			Timeout:           10 * time.Second,
			HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
				key = k
				return errHostKeySeen
			},
		}
		_, _, _, _ = ssh.NewClientConn(conn, address, config)
		_ = conn.Close()
		if key != nil {
			lines = append(lines, knownhosts.Line([]string{knownhosts.Normalize(address)}, key))
		}
	}
	if len(lines) == 0 {
		return "", remoteError{msg: fmt.Sprintf("OpenNeko could not read the SSH host key of %s on port %d.", host, port)}
	}
	return strings.Join(lines, "\n") + "\n", nil
}

var sshKeyTypeNames = map[string]string{
	ssh.KeyAlgoRSA:        "RSA",
	ssh.KeyAlgoDSA:        "DSA",
	ssh.KeyAlgoECDSA256:   "ECDSA",
	ssh.KeyAlgoECDSA384:   "ECDSA",
	ssh.KeyAlgoECDSA521:   "ECDSA",
	ssh.KeyAlgoSKECDSA256: "ECDSA-SK",
	ssh.KeyAlgoED25519:    "ED25519",
	ssh.KeyAlgoSKED25519:  "ED25519-SK",
}

// hostKeyFingerprints is hostKeyFingerprints() in config-vcs/ssh.ts: the
// SHA256 fingerprints that `ssh-keygen -l` prints, as "TYPE SHA256:...".
func hostKeyFingerprints(knownHosts string) []string {
	out := []string{}
	for _, line := range strings.Split(knownHosts, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		_, _, key, _, _, err := ssh.ParseKnownHosts([]byte(line))
		if err != nil {
			continue
		}
		name := sshKeyTypeNames[key.Type()]
		if name == "" {
			name = strings.ToUpper(key.Type())
		}
		out = append(out, name+" "+ssh.FingerprintSHA256(key))
	}
	return out
}

// sshGitEnv is withSshEnv() in config-vcs/ssh.ts: git uses one private key
// and only the pinned host keys. The caller runs cleanup when git is done.
func sshGitEnv(privateKey, knownHosts string) ([]string, func(), error) {
	dir, err := os.MkdirTemp("", "neko-ssh-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}
	if !strings.HasSuffix(privateKey, "\n") {
		privateKey += "\n"
	}
	key, hosts := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(key, []byte(privateKey), 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := os.WriteFile(hosts, []byte(knownHosts), 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	command := strings.Join([]string{"ssh", "-F", "/dev/null", "-i", key,
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + hosts, "-o", "GlobalKnownHostsFile=/dev/null"}, " ")
	return []string{"GIT_SSH_COMMAND=" + command}, cleanup, nil
}
