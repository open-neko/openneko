package compose

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/ownonly"
)

// RemovedServices are the services of earlier releases that this release
// does not run. Compose refuses an override entry for a service with no
// image, so an old entry for one of them would stop every start.
var RemovedServices = []string{
	"embedding",
	"graphjin",
	"graphjin-config-init",
	"graphjin-secret-init",
	"librarian",
	"neko-backup",
	"neko-graphjin",
	"neko-reaper",
	"neko-restore",
	"openshell-certgen",
	"openshell-gateway",
	"openshell-ready",
	"openshell-reg",
	"records-db",
	"records-graphjin",
	"records-graphjin-config-init",
	"records-watch-graphjin",
	"worker",
}

// overrideFile returns the operator's compose.override.yml, or a copy in rt
// without the entries of RemovedServices. The operator's file stays as it is.
func (s *Supervisor) overrideFile(rt string) (string, error) {
	path := filepath.Join(config.Dir(""), "compose.override.yml")
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	kept, ignored, err := withoutRemovedServices(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(ignored) == 0 {
		return path, nil
	}
	dst := filepath.Join(rt, "compose.override.yml")
	if err := os.WriteFile(dst, kept, 0o600); err != nil {
		return "", err
	}
	if err := ownonly.Set(dst); err != nil {
		return "", err
	}
	if !s.overrideNoticed {
		s.overrideNoticed = true
		out := s.Notice
		if out == nil {
			out = os.Stderr
		}
		fmt.Fprintf(out, "warning: %s sets services that this release does not run (%s); OpenNeko ignores those entries. Remove them from the file.\n", path, strings.Join(ignored, ", "))
	}
	return dst, nil
}

func withoutRemovedServices(raw []byte) ([]byte, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return raw, nil, nil
	}
	top := doc.Content[0]
	var services *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "services" && top.Content[i+1].Kind == yaml.MappingNode {
			services = top.Content[i+1]
		}
	}
	if services == nil {
		return raw, nil, nil
	}
	var ignored []string
	var keep []*yaml.Node
	for i := 0; i+1 < len(services.Content); i += 2 {
		if name := services.Content[i].Value; slices.Contains(RemovedServices, name) {
			ignored = append(ignored, name)
			continue
		}
		keep = append(keep, services.Content[i], services.Content[i+1])
	}
	if len(ignored) == 0 {
		return raw, nil, nil
	}
	services.Content = keep
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, nil, err
	}
	if err := yaml.Unmarshal(buf.Bytes(), new(any)); err != nil {
		return nil, nil, fmt.Errorf("the entries of %s hold an anchor that other entries use; remove those entries: %w", strings.Join(ignored, ", "), err)
	}
	slices.Sort(ignored)
	return buf.Bytes(), ignored, nil
}
