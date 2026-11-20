package native

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	getConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	allocConsole          = kernel32.NewProc("AllocConsole")
	freeConsole           = kernel32.NewProc("FreeConsole")
)

// hasConsole is true when the process is attached to a console, also one
// with no window, as on a CI runner.
func hasConsole() bool {
	var pids [4]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n != 0
}

func detachedState() string {
	if hasConsole() {
		return "attached"
	}
	return "detached"
}

// withConsole gives the test a console, so a child that is not detached
// inherits one.
func withConsole(t *testing.T) {
	if hasConsole() {
		return
	}
	if ok, _, err := allocConsole.Call(); ok == 0 {
		t.Fatalf("AllocConsole: %v", err)
	}
	t.Cleanup(func() { _, _, _ = freeConsole.Call() })
}
