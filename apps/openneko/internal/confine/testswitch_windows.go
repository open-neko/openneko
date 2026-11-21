package confine

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var noAppContainer bool

// SetForTest makes this process act as a Windows with no app containers.
// It panics outside a test binary.
func SetForTest(noContainers bool) {
	if !testing.Testing() {
		panic("confine.SetForTest works only in a test binary")
	}
	noAppContainer = noContainers
}

// SetFirewallForTest makes this process see the Windows Firewall service as
// running or not, or fail to read it, until the test ends. It panics outside
// a test binary.
func SetFirewallForTest(t testing.TB, running bool, err error) {
	if !testing.Testing() {
		panic("confine.SetFirewallForTest works only in a test binary")
	}
	old := firewallRunning
	firewallRunning = func() (bool, error) { return running, err }
	t.Cleanup(func() { firewallRunning = old })
}

// StaleProfileForTest makes a runner profile that names serve process pid
// and deletes it when the test ends.
func StaleProfileForTest(t testing.TB, pid int) string {
	t.Helper()
	name, err := profileName(pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createProfile(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deleteProfile(name) })
	return name
}

// ProfileExistsForTest tells whether the app container profile name exists.
func ProfileExistsForTest(name string) bool {
	if !testing.Testing() {
		panic("confine.ProfileExistsForTest works only in a test binary")
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	var sid *windows.SID
	if r, _, _ := procDeriveProfileSID.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&sid))); r != 0 {
		return false
	}
	defer windows.FreeSid(sid)
	k, err := registry.OpenKey(registry.CURRENT_USER, profilesKey+`\`+sid.String(), registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}
