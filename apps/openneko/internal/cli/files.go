package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dosco/graphjin/core/v3/fstable"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/filestore"
)

func newFilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "Manage the store that holds each user's private files",
	}
	cmd.AddCommand(newFilesCopyToBucketCmd())
	return cmd
}

func newFilesCopyToBucketCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "copy-to-bucket",
		Short: "Copy the files in the local files folder to the bucket named by OPENNEKO_FILES_BUCKET",
		Long: "Copy every user file from the local files folder to the bucket that OPENNEKO_FILES_BUCKET names, with the same OPENNEKO_FILES_BUCKET_* settings serve uses. " +
			"A file already in the bucket with the same size is left alone. The local files stay; remove them after you check the bucket.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := filestore.FromEnv(dir)
			if cfg.Bucket == "" {
				return errors.New("OPENNEKO_FILES_BUCKET is not set; set it to s3://bucket[/prefix] with the other bucket settings first")
			}
			store, err := filestore.Open(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if err := store.Check(cmd.Context()); err != nil {
				return err
			}
			copied, skipped, err := copyToStore(cmd.Context(), dir, store)
			fmt.Fprintf(cmd.OutOrStdout(), "%s\nfiles copied: %d\nfiles already in the bucket: %d\n", store.Describe(), copied, skipped)
			return err
		},
	}
	cmd.Flags().StringVar(&dir, "files-dir", envOr("OPENNEKO_FILES_DIR", filepath.Join(dataHome(), "openneko", "files")), "Local files folder to copy from")
	return cmd
}

func copyToStore(ctx context.Context, dir string, store fstable.Backend) (copied, skipped int, err error) {
	root := filepath.Join(dir, "orgs")
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, fs.ErrNotExist) && p == root {
				return nil
			}
			return werr
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".gj-fstable-") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if e, err := store.Stat(ctx, key); err == nil && e.Size == info.Size() {
			skipped++
			return nil
		} else if err != nil && !errors.Is(err, fstable.ErrNotFound) {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = store.Put(ctx, key, f, fstable.PutMeta{})
		f.Close()
		if err != nil {
			return fmt.Errorf("copy %s: %w", key, err)
		}
		copied++
		return nil
	})
	return copied, skipped, err
}

func dataHome() string {
	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		return data
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}
