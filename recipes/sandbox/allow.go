package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const maxEgressHosts = 100

type egressRule struct {
	name     string
	wildcard bool
	port     int
}

// parseEgressHosts reads the admin's list: "host" is port 443, "host:port"
// names the port, and "*.domain" matches each subdomain of domain.
func parseEgressHosts(list []string) ([]egressRule, error) {
	if len(list) > maxEgressHosts {
		return nil, fmt.Errorf("the egress list holds more than %d hosts", maxEgressHosts)
	}
	rules := make([]egressRule, 0, len(list))
	for _, entry := range list {
		rule, err := parseEgressHost(entry)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func parseEgressHost(entry string) (egressRule, error) {
	e := strings.ToLower(strings.TrimSpace(entry))
	bad := fmt.Errorf("invalid egress host %q", entry)
	if e == "" || len(e) > 300 {
		return egressRule{}, bad
	}
	host, port := e, 443
	if h, p, err := net.SplitHostPort(e); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return egressRule{}, bad
		}
		host, port = h, n
	} else if strings.HasPrefix(e, "[") && strings.HasSuffix(e, "]") {
		host = e[1 : len(e)-1]
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return egressRule{}, bad
		}
		return egressRule{name: addr.Unmap().String(), port: port}, nil
	}
	wildcard := strings.HasPrefix(host, "*.")
	name := strings.TrimSuffix(strings.TrimPrefix(host, "*."), ".")
	if !validHostname(name) || wildcard && !strings.Contains(name, ".") {
		return egressRule{}, bad
	}
	return egressRule{name: name, wildcard: wildcard, port: port}, nil
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

func (r egressRule) String() string {
	name := r.name
	if r.wildcard {
		name = "*." + name
	}
	return net.JoinHostPort(name, strconv.Itoa(r.port))
}

// allows matches host as the command named it, before any lookup.
func allows(rules []egressRule, host string, port int) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if addr, err := netip.ParseAddr(host); err == nil {
		host = addr.Unmap().String()
	}
	for _, r := range rules {
		if r.port != port {
			continue
		}
		if r.wildcard && strings.HasSuffix(host, "."+r.name) || !r.wildcard && host == r.name {
			return true
		}
	}
	return false
}

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

// publicAddr refuses loopback, private, link-local (the metadata address),
// shared, documentation, multicast and the IPv6 forms that carry an IPv4
// address inside.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

var errNotPublic = errors.New("the host resolves to an address that is not public")
