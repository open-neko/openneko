package native

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func systemPID(t *testing.T) int {
	t.Helper()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snap)
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "services.exe") {
			return int(e.ProcessID)
		}
	}
	t.Fatal("control: no services.exe runs here")
	return 0
}

func TestAliveRefusesAProcessOfAnotherAccount(t *testing.T) {
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if me.User.Sid.String() == "S-1-5-18" {
		t.Fatal("control: the test runs as SYSTEM, the account of services.exe")
	}
	if Alive(systemPID(t)) {
		t.Error("Alive reports the SYSTEM process services.exe as ours")
	}
}

func TestStopEventRefusesATakenName(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = helperEnviron("stubborn", "")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	squat, err := windows.CreateEvent(nil, 1, 0, stopEventName(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(squat)
	if ev, err := stopEvent(cmd.Process.Pid); err == nil {
		windows.CloseHandle(ev)
		t.Fatal("stopEvent takes a name that another process made")
	}

	var log strings.Builder
	defer func(w io.Writer) { stopLog = w }(stopLog)
	stopLog = &log
	own, err := windows.CreateEvent(nil, 1, 0, stopEventName(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(own)
	_, stop := NotifyStop(context.Background())
	stop()
	if !strings.Contains(log.String(), "no stop event") {
		t.Fatalf("NotifyStop said nothing about its missing stop event: %q", log.String())
	}
}

func TestStopEventIsClosedToOtherAccounts(t *testing.T) {
	ev, err := stopEvent(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ev)
	sd, err := windows.GetSecurityInfo(ev, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("the stop event has no access list: %v", err)
	}
	me, _ := windows.GetCurrentProcessToken().GetTokenUser()
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if sid != "S-1-5-18" && sid != me.User.Sid.String() {
			t.Errorf("the stop event allows %s (%s)", sid, sd)
		}
	}
}

var isProcessInJob = kernel32.NewProc("IsProcessInJob")

func inJob(t *testing.T, pid int, j *job) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var in int32
	if ok, _, err := isProcessInJob.Call(uintptr(h), uintptr(j.h), uintptr(unsafe.Pointer(&in))); ok == 0 {
		t.Fatal(err)
	}
	return in != 0
}

func TestAGrandchildStartsInsideTheJob(t *testing.T) {
	j, err := newJob()
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = helperEnviron("spawner", pidFile)
	cmd.SysProcAttr = childAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(2 * time.Second)
	if err := j.add(cmd.Process); err != nil {
		t.Fatal(err)
	}
	var grandchild int
	waitFor(t, "the grandchild starts", 30*time.Second, func() bool {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		grandchild, err = strconv.Atoi(string(raw))
		return err == nil && grandchild > 0
	})
	t.Cleanup(func() {
		if p, err := os.FindProcess(grandchild); err == nil {
			_ = p.Kill()
		}
	})
	if !inJob(t, grandchild, j) {
		t.Fatal("the child started a process before it joined the job, so a stop leaves that process running")
	}
	j.close()
	waitFor(t, "the grandchild ends with the job", 10*time.Second, func() bool { return !Alive(grandchild) })
}
