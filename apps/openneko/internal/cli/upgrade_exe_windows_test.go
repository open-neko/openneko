package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestSwapWaitsOutAHandleWithoutDeleteSharing: a scanner that holds the
// installed executable for a moment does not fail the upgrade.
func TestSwapWaitsOutAHandleWithoutDeleteSharing(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "openneko.exe")
	staged := exe + ".new"
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(exe)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe, exe+".probe"); !sharingViolation(err) {
		windows.CloseHandle(h)
		t.Fatalf("control: the held executable renamed with %v, so this test proves nothing", err)
	}
	released := time.AfterFunc(time.Second, func() { windows.CloseHandle(h) })
	defer released.Stop()
	if err := swapAside(staged, exe); err != nil {
		t.Fatalf("the swap gave up while a scanner held the executable: %v", err)
	}
	if got := readFileOr(exe, ""); got != "v2" {
		t.Fatalf("the executable holds %q, want the new release", got)
	}
}
