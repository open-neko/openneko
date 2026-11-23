// Package s3test starts a SeaweedFS S3 server for tests that set
// OPENNEKO_BUCKET_IT=1.
package s3test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	Image  = "chrislusf/seaweedfs:latest@sha256:ccad23e18639c8b98cee17e7100b95830a06077b56d8e5b006d6dcf8ec5e94a0"
	KeyID  = "testkey"
	Secret = "testsecret"
)

const identities = `{"identities":[{"name":"test","credentials":[{"accessKey":"testkey","secretKey":"testsecret"}],"actions":["Admin","Read","Write","List","Tagging"]}]}`

// Start returns the server's endpoint. It skips the test unless
// OPENNEKO_BUCKET_IT=1.
func Start(t *testing.T) string {
	t.Helper()
	if os.Getenv("OPENNEKO_BUCKET_IT") != "1" {
		t.Skip("set OPENNEKO_BUCKET_IT=1 to run against a SeaweedFS S3 server in Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        Image,
			ExposedPorts: []string{"8333/tcp"},
			Cmd:          []string{"server", "-s3", "-s3.config=/etc/s3.json", "-dir=/data", "-master.volumeSizeLimitMB=64", "-volume.max=8"},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(identities),
				ContainerFilePath: "/etc/s3.json",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForHTTP("/").WithPort("8333/tcp").
				WithStatusCodeMatcher(func(int) bool { return true }).
				WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("seaweedfs container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "8333/tcp")
	if err != nil {
		t.Fatal(err)
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// Bucket creates a bucket and returns its name.
func Bucket(t *testing.T, endpoint, name string) string {
	t.Helper()
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(KeyID, Secret, ""),
	})
	deadline := time.Now().Add(time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
		cancel()
		if err == nil {
			return name
		}
		if time.Now().After(deadline) {
			t.Fatalf("create bucket %s: %v", name, err)
		}
		time.Sleep(time.Second)
	}
}
