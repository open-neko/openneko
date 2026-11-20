package webfront

import (
	"context"
	"errors"
	"log"
	"slices"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

// egressHostsOf reads the stored list. A row from before the setting, or a
// list that no longer passes the check, gives an empty list.
func egressHostsOf(cfg map[string]any) []string {
	raw, _ := cfg["egressHosts"].([]any)
	list := make([]string, 0, len(raw))
	for _, v := range raw {
		host, ok := v.(string)
		if !ok {
			return []string{}
		}
		list = append(list, host)
	}
	hosts, err := sandbox.NormalizeEgressHosts(list)
	if err != nil {
		return []string{}
	}
	return hosts
}

func egressHostsFrom(raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("egressHosts must be a list of host names")
	}
	list := make([]string, 0, len(items))
	for _, v := range items {
		host, ok := v.(string)
		if !ok {
			return nil, errors.New("egressHosts must be a list of host names")
		}
		list = append(list, host)
	}
	return sandbox.NormalizeEgressHosts(list)
}

// orgEgressHosts is the list the sandbox's proxy lets the org's runs reach.
func (a *API) orgEgressHosts(ctx context.Context, orgID string) ([]string, error) {
	_, cfg, _, err := a.loadAgentRow(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return egressHostsOf(cfg), nil
}

// packEgressSuggestions offers the hosts that installed packs declare in
// permissions.network. They join the list only when an admin adds them.
func (a *API) packEgressSuggestions(ctx context.Context, orgID string, listed []string) []string {
	rows, err := a.DB.Query(ctx, `select pack_id, version from pack_install where org_id = $1 and status <> 'removed' order by pack_id`, orgID)
	if err != nil {
		log.Printf("pack egress hosts unavailable: %v", err)
		return []string{}
	}
	type installed struct{ id, version string }
	var packs []installed
	for rows.Next() {
		var p installed
		if rows.Scan(&p.id, &p.version) == nil {
			packs = append(packs, p)
		}
	}
	rows.Close()
	out := []string{}
	for _, p := range packs {
		bundle, err := packBundle(orgID, p.id, p.version)
		if err != nil || bundle.Manifest == nil {
			continue
		}
		for _, host := range bundle.Manifest.Permissions.Network {
			normal, err := sandbox.NormalizeEgressHosts([]string{host})
			if err == nil && !slices.Contains(listed, normal[0]) && !slices.Contains(out, normal[0]) {
				out = append(out, normal[0])
			}
		}
	}
	return out
}

func anyList(list []string) []any {
	out := make([]any, len(list))
	for i, v := range list {
		out[i] = v
	}
	return out
}
