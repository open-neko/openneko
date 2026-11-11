// Package filestore opens the store that holds each user's private files:
// a local folder, or a bucket on any S3-compatible service.
package filestore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dosco/graphjin/core/v3/fstable"
)

type Config struct {
	Dir         string
	Bucket      string
	Endpoint    string
	Region      string
	AccessKeyID string
	SecretKey   string
}

func FromEnv(dir string) Config {
	return Config{
		Dir:         dir,
		Bucket:      strings.TrimSpace(os.Getenv("OPENNEKO_FILES_BUCKET")),
		Endpoint:    strings.TrimSpace(os.Getenv("OPENNEKO_FILES_BUCKET_ENDPOINT")),
		Region:      strings.TrimSpace(os.Getenv("OPENNEKO_FILES_BUCKET_REGION")),
		AccessKeyID: strings.TrimSpace(os.Getenv("OPENNEKO_FILES_BUCKET_KEY_ID")),
		SecretKey:   strings.TrimSpace(os.Getenv("OPENNEKO_FILES_BUCKET_SECRET")),
	}
}

type Store interface {
	fstable.Backend
	Describe() string
	Check(ctx context.Context) error
}

func Open(ctx context.Context, c Config) (Store, error) {
	if c.Bucket == "" {
		if c.Endpoint != "" || c.AccessKeyID != "" || c.SecretKey != "" {
			return nil, errors.New("OPENNEKO_FILES_BUCKET_* is set but OPENNEKO_FILES_BUCKET is empty; set the bucket or remove the other bucket settings")
		}
		return openLocal(c.Dir)
	}
	return openBucket(ctx, c)
}

type local struct {
	*fstable.Local
	dir string
}

func openLocal(dir string) (Store, error) {
	if dir == "" {
		return nil, errors.New("the files folder is not set")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	l, err := fstable.NewLocal(fstable.LocalConfig{Root: dir})
	if err != nil {
		return nil, err
	}
	return &local{Local: l, dir: dir}, nil
}

func (l *local) Describe() string { return "folder " + l.dir }

func (l *local) Check(context.Context) error {
	st, err := os.Stat(l.dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a folder", l.dir)
	}
	return nil
}

func parseBucket(raw string) (name, prefix string, err error) {
	rest, ok := strings.CutPrefix(raw, "s3://")
	if !ok {
		return "", "", fmt.Errorf("OPENNEKO_FILES_BUCKET must look like s3://bucket or s3://bucket/prefix, got %q", raw)
	}
	name, prefix, _ = strings.Cut(rest, "/")
	if name == "" {
		return "", "", errors.New("OPENNEKO_FILES_BUCKET names no bucket")
	}
	prefix = strings.Trim(prefix, "/")
	for _, seg := range strings.Split(prefix, "/") {
		if seg == "." || seg == ".." {
			return "", "", fmt.Errorf("OPENNEKO_FILES_BUCKET prefix %q holds a . or .. part", prefix)
		}
	}
	if prefix != "" {
		prefix += "/"
	}
	return name, prefix, nil
}

func parseEndpoint(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("OPENNEKO_FILES_BUCKET_ENDPOINT must be a plain https://host[:port] URL, got %q", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopback(u.Hostname()) {
			return "", fmt.Errorf("OPENNEKO_FILES_BUCKET_ENDPOINT %s sends files without TLS; use https (http is accepted only for localhost)", raw)
		}
	default:
		return "", fmt.Errorf("OPENNEKO_FILES_BUCKET_ENDPOINT must use https, got %q", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// LocalFilesLeft counts the files in dir that a switch to a bucket leaves
// behind, so serve can warn that they were not copied.
func LocalFilesLeft(dir string, limit int) int {
	n := 0
	root := filepath.Join(dir, "orgs")
	errStop := errors.New("stop")
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			n++
			if n >= limit {
				return errStop
			}
		}
		return nil
	})
	return n
}

const checkTimeout = 5 * time.Second
