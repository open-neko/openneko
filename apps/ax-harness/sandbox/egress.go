package sandbox

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// MaxEgressHosts matches maxEgressHosts in recipes/sandbox/allow.go.
const MaxEgressHosts = 100

// Egress is what the sandbox's proxy lets a run reach, and the names its
// log gives the run.
type Egress struct {
	Org   string   `json:"org"`
	Label string   `json:"label"`
	Hosts []string `json:"hosts"`
}

// NormalizeEgressHosts checks an admin's list by the rules of
// parseEgressHost in recipes/sandbox/allow.go and returns it in one form:
// "host" for port 443, "host:port" otherwise, without repeats.
func NormalizeEgressHosts(list []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, entry := range list {
		host, err := normalizeEgressHost(entry)
		if err != nil {
			return nil, err
		}
		if !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	if len(out) > MaxEgressHosts {
		return nil, fmt.Errorf("the egress list holds more than %d hosts", MaxEgressHosts)
	}
	return out, nil
}

func normalizeEgressHost(entry string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(entry))
	bad := fmt.Errorf("%q is not a host name, *.domain, or an address, with an optional :port", strings.TrimSpace(entry))
	if e == "" || len(e) > 300 {
		return "", bad
	}
	host, port := e, 443
	if h, p, err := net.SplitHostPort(e); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return "", bad
		}
		host, port = h, n
	} else if strings.HasPrefix(e, "[") && strings.HasSuffix(e, "]") {
		host = e[1 : len(e)-1]
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", bad
		}
		host = addr.Unmap().String()
		if port == 443 {
			if addr.Unmap().Is6() {
				return "[" + host + "]", nil
			}
			return host, nil
		}
		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}
	wildcard := strings.HasPrefix(host, "*.")
	name := strings.TrimSuffix(strings.TrimPrefix(host, "*."), ".")
	if !validHostname(name) || wildcard && !strings.Contains(name, ".") {
		return "", bad
	}
	if wildcard {
		name = "*." + name
	}
	if port == 443 {
		return name, nil
	}
	return name + ":" + strconv.Itoa(port), nil
}

func validHostname(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	labels := strings.Split(name, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	_, err := strconv.Atoi(labels[len(labels)-1])
	return err != nil
}

// SetEgress names the run for the proxy's log and gives the hosts its
// commands may reach. A run with no call reaches no host.
func (c *Client) SetEgress(e Egress) error {
	hosts, err := NormalizeEgressHosts(e.Hosts)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.egress = Egress{Org: egressLabel(e.Org), Label: egressLabel(e.Label), Hosts: hosts}
	return nil
}

func egressLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() == 128 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
