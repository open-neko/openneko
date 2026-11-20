package native

import (
	"testing"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	allocConsole     = kernel32.NewProc("AllocConsole")
	freeConsole      = kernel32.NewProc("FreeConsole")
)

func detachedState() string {
	if h, _, _ := getConsoleWindow.Call(); h == 0 {
		return "detached"
	}
	return "attached"
}

// withConsole gives the test a console, so a child that is not detached
// inherits one.
func withConsole(t *testing.T) {
	if h, _, _ := getConsoleWindow.Call(); h != 0 {
		return
	}
	if ok, _, err := allocConsole.Call(); ok == 0 {
		t.Fatalf("AllocConsole: %v", err)
	}
	t.Cleanup(func() { _, _, _ = freeConsole.Call() })
}
