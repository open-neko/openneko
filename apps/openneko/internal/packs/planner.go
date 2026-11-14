package packs

import "slices"

type InstalledArtifact struct {
	Kind            ArtifactKind
	Key             string
	TargetRef       string
	DesiredHash     string
	LastAppliedHash string
	CurrentHash     *string
	Ownership       string
}

type PlanEntry struct {
	Action      string       `json:"action"`
	Kind        ArtifactKind `json:"kind"`
	Key         string       `json:"key"`
	TargetRef   string       `json:"targetRef"`
	DesiredHash *string      `json:"desiredHash"`
	CurrentHash *string      `json:"currentHash"`
	Reason      string       `json:"reason,omitempty"`
}

type Plan struct {
	PackID     string      `json:"packId"`
	Version    string      `json:"version"`
	BundleHash string      `json:"bundleHash"`
	Entries    []PlanEntry `json:"entries"`
	Hash       string      `json:"hash"`
}

func sameHash(a *string, b string) bool { return a != nil && *a == b }

func desiredEntry(a Artifact, installed *InstalledArtifact) PlanEntry {
	desired := a.Hash
	e := PlanEntry{Kind: a.Kind, Key: a.Key, TargetRef: a.TargetRef, DesiredHash: &desired}
	if installed == nil {
		e.Action = "create"
		return e
	}
	e.CurrentHash = installed.CurrentHash
	switch {
	case installed.Ownership == "detached":
		e.Action, e.Reason = "conflict", "detached"
	case installed.Ownership == "retired":
		if installed.CurrentHash != nil && !sameHash(installed.CurrentHash, installed.LastAppliedHash) {
			e.Action, e.Reason = "conflict", "user_modified"
		} else {
			e.Action = "update"
		}
	case installed.CurrentHash == nil:
		e.Action, e.Reason = "conflict", "target_missing"
	case *installed.CurrentHash != installed.LastAppliedHash:
		e.Action, e.Reason = "conflict", "user_modified"
	case installed.DesiredHash == a.Hash:
		e.Action = "noop"
	default:
		e.Action = "update"
	}
	return e
}

// PlanBundle is planPack().
func PlanBundle(b *Bundle, installedArtifacts []InstalledArtifact) Plan {
	installed := map[string]*InstalledArtifact{}
	var order []string
	for i := range installedArtifacts {
		id := string(installedArtifacts[i].Kind) + ":" + installedArtifacts[i].Key
		if _, seen := installed[id]; !seen {
			order = append(order, id)
		}
		installed[id] = &installedArtifacts[i]
	}
	entries := []PlanEntry{}
	for _, a := range b.Artifacts {
		id := string(a.Kind) + ":" + a.Key
		entries = append(entries, desiredEntry(a, installed[id]))
		delete(installed, id)
	}
	for _, id := range order {
		a, ok := installed[id]
		if !ok || a.Ownership == "detached" || a.Ownership == "retired" {
			continue
		}
		modified := !sameHash(a.CurrentHash, a.LastAppliedHash)
		e := PlanEntry{Kind: a.Kind, Key: a.Key, TargetRef: a.TargetRef, CurrentHash: a.CurrentHash, Action: "retire", Reason: "removed_from_pack"}
		if modified {
			e.Action, e.Reason = "conflict", "user_modified"
		}
		entries = append(entries, e)
	}
	slices.SortStableFunc(entries, func(x, y PlanEntry) int {
		return localeCompare(string(x.Kind)+":"+x.Key, string(y.Kind)+":"+y.Key)
	})
	p := Plan{PackID: b.Manifest.Metadata.ID, Version: b.Manifest.Metadata.Version, BundleHash: b.BundleHash, Entries: entries}
	p.Hash = mustHash(map[string]any{"packId": p.PackID, "version": p.Version, "bundleHash": p.BundleHash, "entries": entries})
	return p
}

// Permissions is declarativePackPermissions(): what the review card shows.
func Permissions(b *Bundle) map[string]string {
	database, apiWrite := "none", "not requested"
	for _, a := range b.Artifacts {
		if a.Kind != KindSource {
			continue
		}
		src := a.Record()
		switch src["kind"] {
		case "database":
			database = "read-only"
		case "api":
			caps, _ := src["capabilities"].(map[string]any)
			if caps["api.write"] == true || caps["api.delete"] == true {
				apiWrite = "requested; actions require an enabled policy"
			}
		}
	}
	out := map[string]string{"database": database, "apiWrite": apiWrite}
	if n := len(b.Manifest.OAuth); n > 0 {
		scopes := map[string]bool{}
		for _, c := range b.Manifest.OAuth {
			for _, s := range c.Scopes {
				scopes[s] = true
			}
		}
		out["oauth"] = itoa(n) + " account connection; " + itoa(len(scopes)) + " consent scopes"
	}
	if len(b.Manifest.Permissions.Network) > 0 {
		out["network"] = joinComma(b.Manifest.Permissions.Network)
	}
	return out
}
