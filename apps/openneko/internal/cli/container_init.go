package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/open-neko/neko/apps/openneko/internal/ownonly"
	"github.com/spf13/cobra"
)

func newContainerInitCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:    "container-init",
		Short:  "Seed config.json and secret-key from NEKO_PG_* and NEKO_SECRET_KEY",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return seedContainerConfig(dir, os.Getenv("OPENNEKO_FORCE_CONFIG") == "1")
		},
	}
	cmd.Flags().StringVar(&dir, "config-dir", "", "Config folder")
	_ = cmd.MarkFlagRequired("config-dir")
	return cmd
}

func seedContainerConfig(dir string, force bool) error {
	envPg := map[string]any{}
	for _, f := range []struct{ env, key string }{
		{"NEKO_PG_HOST", "host"}, {"NEKO_PG_USER", "user"},
		{"NEKO_PG_DATABASE", "database"}, {"NEKO_PG_SSLMODE", "sslmode"},
	} {
		if v := os.Getenv(f.env); v != "" {
			envPg[f.key] = v
		}
	}
	if v := os.Getenv("NEKO_PG_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("NEKO_PG_PORT %q is not a number", v)
		}
		envPg["port"] = port
	}
	if v, ok := os.LookupEnv("NEKO_PG_PASSWORD"); ok {
		envPg["password"] = v
	}

	if len(envPg) > 0 {
		path := filepath.Join(dir, "config.json")
		current := map[string]any{}
		if raw, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(raw, &current) != nil || current == nil {
				current = map[string]any{}
			}
		}
		next := make(map[string]any, len(current)+1)
		for k, v := range current {
			next[k] = v
		}
		pg := map[string]any{}
		if old, ok := current["pg"].(map[string]any); ok {
			for k, v := range old {
				pg[k] = v
			}
		}
		for k, v := range envPg {
			if old, ok := pg[k]; force || !ok || old == nil || old == "" {
				pg[k] = v
			}
		}
		next["pg"] = pg
		was, _ := json.Marshal(current)
		now, _ := json.Marshal(next)
		if !bytes.Equal(was, now) {
			out, err := json.MarshalIndent(next, "", "  ")
			if err != nil {
				return err
			}
			if err := writePrivateFile(path, append(out, '\n'), os.O_TRUNC); err != nil {
				return err
			}
		}
	}

	secretPath := filepath.Join(dir, "secret-key")
	if _, err := os.Stat(secretPath); !force && err == nil {
		return nil
	}
	value := os.Getenv("NEKO_SECRET_KEY")
	if value == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		value = base64.StdEncoding.EncodeToString(buf)
	}
	mode := os.O_EXCL
	if force {
		mode = os.O_TRUNC
	}
	if err := writePrivateFile(secretPath, []byte(value), mode); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

func writePrivateFile(path string, data []byte, flag int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|flag, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return ownonly.Set(path)
}
