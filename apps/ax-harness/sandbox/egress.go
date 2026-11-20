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
		if !publicAddr(addr) {
			return "", fmt.Errorf("%q is not a public address: the sandbox's proxy refuses private, loopback, link-local and metadata addresses", strings.TrimSpace(entry))
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

// nonPublic matches nonPublic in recipes/sandbox/allow.go.
var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/32", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
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
