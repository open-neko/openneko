package host

import "testing"

func TestDarwinArm64(t *testing.T) {
	r := checkWith("darwin", "arm64", func() bool { return false })
	if !r.Supported {
		t.Fatal("darwin arm64 should be supported regardless of KVM")
	}
	if r.Triple != "darwin-arm64" {
		t.Fatalf("bad triple: %s", r.Triple)
	}
}

func TestDarwinAmd64(t *testing.T) {
	r := checkWith("darwin", "amd64", func() bool { return false })
	if r.Supported {
		t.Fatal("darwin amd64 should not be supported")
	}
	if r.Reason == "" {
		t.Fatal("missing reason")
	}
}

func TestLinuxNoKVM(t *testing.T) {
	r := checkWith("linux", "amd64", func() bool { return false })
	if r.Supported {
		t.Fatal("linux without /dev/kvm should not be supported")
	}
	if r.Triple != "linux-x64-gnu" {
		t.Fatalf("bad triple: %s", r.Triple)
	}
}

func TestLinuxAmd64WithKVM(t *testing.T) {
	r := checkWith("linux", "amd64", func() bool { return true })
	if !r.Supported {
		t.Fatal("linux amd64 with /dev/kvm should be supported")
	}
}

func TestLinuxArm64WithKVM(t *testing.T) {
	r := checkWith("linux", "arm64", func() bool { return true })
	if !r.Supported || r.Triple != "linux-arm64-gnu" {
		t.Fatalf("unexpected: %+v", r)
	}
}

func TestLinuxUnknownArch(t *testing.T) {
	r := checkWith("linux", "riscv64", func() bool { return true })
	if r.Supported {
		t.Fatal("unknown arch should not be supported")
	}
}

func TestWindows(t *testing.T) {
	r := checkWith("windows", "amd64", func() bool { return false })
	if r.Supported {
		t.Fatal("windows should not be supported")
	}
}
