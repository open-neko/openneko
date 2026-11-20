package cli

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestNativeDoctorChecksTheNativeRelease(t *testing.T) {
	isolateNamedInstallation(t, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENNEKO_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	var out bytes.Buffer
	_ = runNativeDoctor(context.Background(), &out)
	first, _, _ := strings.Cut(out.String(), "\n")
	if want := "host: " + runtime.GOOS + "-" + runtime.GOARCH + " (supported)"; first != want {
		t.Fatalf("first line %q, want %q\n%s", first, want, out.String())
	}
}

func TestPostgresToolHintOnWindows(t *testing.T) {
	err := postgresToolMissing("windows", "pg_dump", "17").Error()
	if !strings.Contains(err, `C:\Program Files\PostgreSQL\17\bin`) || !strings.Contains(err, "PATH") {
		t.Fatalf("windows: %s", err)
	}
	if err := postgresToolMissing("windows", "pg_restore", "").Error(); !strings.Contains(err, `PostgreSQL\<version>\bin`) {
		t.Fatalf("windows, no server version: %s", err)
	}
	if err := postgresToolMissing("linux", "pg_dump", "17").Error(); err != "pg_dump was not found; install the PostgreSQL 17 client tools so that backup and restore work" {
		t.Fatalf("linux: %s", err)
	}
}
