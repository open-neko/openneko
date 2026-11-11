package filestore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/dosco/graphjin/core/v3/fstable"
)

type bucket struct {
	client *s3.Client
	name   string
	prefix string
	where  string
	spool  string
}

func openBucket(ctx context.Context, c Config) (Store, error) {
	name, prefix, err := parseBucket(c.Bucket)
	if err != nil {
		return nil, err
	}
	endpoint, err := parseEndpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	if (c.AccessKeyID == "") != (c.SecretKey == "") {
		return nil, errors.New("set both OPENNEKO_FILES_BUCKET_KEY_ID and OPENNEKO_FILES_BUCKET_SECRET, or neither to use the host's AWS credentials")
	}
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if c.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("bucket settings: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	where := "bucket s3://" + name + "/" + prefix
	if endpoint != "" {
		where += " at " + endpoint
	}
	spool := c.Dir
	if spool == "" {
		spool = os.TempDir()
	}
	return &bucket{client: client, name: name, prefix: prefix, where: where, spool: spool}, nil
}

func (b *bucket) Name() string     { return "s3" }
func (b *bucket) Describe() string { return b.where }

func (b *bucket) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	_, err := b.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &b.name})
	if err != nil {
		return fmt.Errorf("%s: %w", b.where, plain(err))
	}
	return nil
}

func (b *bucket) object(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\x00") {
		return "", fmt.Errorf("invalid key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid key %q", key)
		}
	}
	return b.prefix + key, nil
}

func (b *bucket) entry(key string, size *int64, etag, ctype *string, mod *time.Time) fstable.Entry {
	e := fstable.Entry{Key: key, Size: aws.ToInt64(size), ETag: strings.Trim(aws.ToString(etag), `"`), ContentType: aws.ToString(ctype)}
	if mod != nil {
		e.ModifiedAt = *mod
	}
	if e.ContentType == "" {
		e.ContentType = guessType(key)
	}
	return e
}

func (b *bucket) List(ctx context.Context, opts fstable.ListOpts) ([]fstable.Entry, string, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	in := &s3.ListObjectsV2Input{
		Bucket:  &b.name,
		Prefix:  aws.String(b.prefix + strings.TrimPrefix(opts.Prefix, "/")),
		MaxKeys: aws.Int32(int32(limit)),
	}
	if opts.After != "" {
		in.ContinuationToken = aws.String(opts.After)
	}
	out, err := b.client.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("list %s: %w", b.where, plain(err))
	}
	entries := make([]fstable.Entry, 0, len(out.Contents))
	for _, o := range out.Contents {
		key, ok := strings.CutPrefix(aws.ToString(o.Key), b.prefix)
		if !ok || key == "" || strings.HasSuffix(key, "/") {
			continue
		}
		entries = append(entries, b.entry(key, o.Size, o.ETag, nil, o.LastModified))
	}
	next := ""
	if aws.ToBool(out.IsTruncated) {
		next = aws.ToString(out.NextContinuationToken)
	}
	return entries, next, nil
}

func (b *bucket) Stat(ctx context.Context, key string) (fstable.Entry, error) {
	obj, err := b.object(key)
	if err != nil {
		return fstable.Entry{}, err
	}
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &b.name, Key: &obj})
	if err != nil {
		if notFound(err) {
			return fstable.Entry{}, fstable.ErrNotFound
		}
		return fstable.Entry{}, fmt.Errorf("stat %s: %w", key, plain(err))
	}
	return b.entry(key, out.ContentLength, out.ETag, out.ContentType, out.LastModified), nil
}

func (b *bucket) Get(ctx context.Context, key string) (io.ReadCloser, fstable.Entry, error) {
	obj, err := b.object(key)
	if err != nil {
		return nil, fstable.Entry{}, err
	}
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &b.name, Key: &obj})
	if err != nil {
		if notFound(err) {
			return nil, fstable.Entry{}, fstable.ErrNotFound
		}
		return nil, fstable.Entry{}, fmt.Errorf("read %s: %w", key, plain(err))
	}
	return out.Body, b.entry(key, out.ContentLength, out.ETag, out.ContentType, out.LastModified), nil
}

// Put copies the body to a spool file first: the SDK signs a body it can
// seek, and a body that fails part way must not replace the stored object.
func (b *bucket) Put(ctx context.Context, key string, body io.Reader, meta fstable.PutMeta) (fstable.Entry, error) {
	obj, err := b.object(key)
	if err != nil {
		return fstable.Entry{}, err
	}
	if err := os.MkdirAll(b.spool, 0o700); err != nil {
		return fstable.Entry{}, err
	}
	tmp, err := os.CreateTemp(b.spool, ".openneko-upload-*")
	if err != nil {
		return fstable.Entry{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, body)
	if err != nil {
		return fstable.Entry{}, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fstable.Entry{}, err
	}
	ctype := meta.ContentType
	if ctype == "" {
		ctype = guessType(key)
	}
	_, err = b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &b.name,
		Key:           &obj,
		Body:          tmp,
		ContentLength: aws.Int64(n),
		ContentType:   aws.String(ctype),
	})
	if err != nil {
		return fstable.Entry{}, fmt.Errorf("write %s: %w", key, plain(err))
	}
	e, err := b.Stat(ctx, key)
	if err != nil {
		return fstable.Entry{}, err
	}
	return e, nil
}

func (b *bucket) Delete(ctx context.Context, key string) error {
	obj, err := b.object(key)
	if err != nil {
		return err
	}
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b.name, Key: &obj}); err != nil && !notFound(err) {
		return fmt.Errorf("delete %s: %w", key, plain(err))
	}
	return nil
}

func (b *bucket) Presign(context.Context, string, fstable.PresignOp, time.Duration) (string, error) {
	return "", fstable.ErrUnsupported
}

func notFound(err error) bool {
	var nk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nk) || errors.As(err, &nf) {
		return true
	}
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey")
}

// plain keeps the service's error code and message and drops the SDK's
// request dump, which repeats the endpoint and request ids.
func plain(err error) error {
	var api smithy.APIError
	if errors.As(err, &api) {
		return fmt.Errorf("%s: %s", api.ErrorCode(), api.ErrorMessage())
	}
	return err
}

func guessType(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	case ".txt", ".md":
		return "text/plain"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	return "application/octet-stream"
}
