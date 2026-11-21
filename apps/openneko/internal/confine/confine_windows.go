package confine

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runnerMechanism = "appcontainer"

// Stacks is false: the agent runner does not confine itself on Windows;
// serve starts it in an app container.
const Stacks = false

// minBuild is Windows 10 1809 and Server 2019: the first builds with Unix
// sockets that an app container can reach and that name their peer.
const minBuild = 17763

var runnerGaps = []string{"the files, folders and registry keys that Windows opens to every app container, such as the system folders"}

var (
	userenv              = windows.NewLazySystemDLL("userenv.dll")
	procCreateProfile    = userenv.NewProc("CreateAppContainerProfile")
	procDeleteProfile    = userenv.NewProc("DeleteAppContainerProfile")
	procDeriveProfileSID = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
)

const (
	tokenIsAppContainer      = 29
	tokenCapabilities        = 30
	attrSecurityCapabilities = 0x00020009
	profilePrefix            = "OpenNeko.Runner."
	fileDeleteChild          = 0x40
	profilesKey              = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppContainer\Mappings`
)

// Available tells that this build does not confine the network part of
// openneko serve on Windows.
func Available() State {
	return State{Reason: fmt.Sprintf("confinement on %s is not built yet", runtime.GOOS)}
}

func Kernel() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("Windows %d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}

func Apply(Spec) (State, error) {
	st := Available()
	return st, fmt.Errorf("%w: %s", ErrUnsupported, st.Reason)
}

func Program(path string) (exec, read []string) { return []string{path}, nil }

func InContainer() bool { return false }

func LSMList() (string, error) { return "", ErrUnsupported }

// RunnerAvailable tells whether this system can start an agent runner in an
// app container, with no change to the system.
func RunnerAvailable() State {
	st := State{Mechanism: runnerMechanism}
	if noAppContainer {
		st.Reason = Kernel() + " has no app containers"
		return st
	}
	for _, p := range []*windows.LazyProc{procCreateProfile, procDeleteProfile, procDeriveProfileSID} {
		if p.Find() != nil {
			st.Reason = Kernel() + " has no app containers"
			return st
		}
	}
	if b := windows.RtlGetVersion().BuildNumber; b < minBuild {
		st.Reason = fmt.Sprintf("%s has no Unix sockets for app containers; use Windows 10 1809, Windows Server 2019 or later", Kernel())
		return st
	}
	st.Confined, st.Gaps = true, runnerGaps
	return st
}

// ApplyRunner checks, in the agent runner, that serve started it in an app
// container with no capabilities. On an error the runner must not run
// agent code.
func ApplyRunner(Spec) (State, error) {
	st := RunnerAvailable()
	if !st.Confined {
		return st, fmt.Errorf("%w: %s", ErrUnsupported, st.Reason)
	}
	if err := inAppContainer(); err != nil {
		st.Confined, st.Gaps, st.Reason = false, nil, err.Error()
		return st, err
	}
	return st, nil
}

func inAppContainer() error {
	tok := windows.GetCurrentProcessToken()
	var in, n uint32
	if err := windows.GetTokenInformation(tok, tokenIsAppContainer, (*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)), &n); err != nil {
		return fmt.Errorf("read the runner's token: %w", err)
	}
	if in == 0 {
		return errors.New("this agent runner is not in an app container")
	}
	buf := make([]byte, 4096)
	if err := windows.GetTokenInformation(tok, tokenCapabilities, &buf[0], uint32(len(buf)), &n); err != nil {
		return fmt.Errorf("read the runner's capabilities: %w", err)
	}
	if groups := (*windows.Tokengroups)(unsafe.Pointer(&buf[0])); groups.GroupCount != 0 {
		return fmt.Errorf("this agent runner holds %d app container capabilities", groups.GroupCount)
	}
	return nil
}

// Limits bound one runner through its job object.
type Limits struct {
	CPUSeconds uint64
	Memory     uint64
}

// Runner is an agent runner in an app container of its own.
type Runner struct {
	pid     int
	process windows.Handle
	job     windows.Handle
	release func()
	once    sync.Once
}

func (r *Runner) Pid() int { return r.pid }

func (r *Runner) Wait() error {
	if _, err := windows.WaitForSingleObject(r.process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(r.process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("exit status %d", code)
	}
	return nil
}

// Kill ends the runner and anything in its job.
func (r *Runner) Kill() error { return windows.TerminateJobObject(r.job, 1) }

// Close ends the job, takes back the access that the start granted and
// deletes the runner's app container profile.
func (r *Runner) Close() error {
	r.once.Do(func() {
		_ = windows.CloseHandle(r.job)
		_ = windows.CloseHandle(r.process)
		r.release()
	})
	return nil
}

// grantMu keeps two starts from editing one access list at once.
var grantMu sync.Mutex

// Access that a runner gets on its paths. It never gets WRITE_DAC or
// WRITE_OWNER, so it cannot open a file it made to another app container.
const (
	readAccess   = windows.FILE_GENERIC_READ
	modifyAccess = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE | fileDeleteChild
	execAccess   = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE
	linkAccess   = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE
)

// StartRunner starts an agent runner in a new app container with no
// capabilities: no network, no file outside the paths of spec, and no
// process of this account. The container's SID gets spec.ReadWrite to read
// and write, spec.Read to read, spec.Exec to run and the folders of
// spec.Sockets to connect. A job object kills the runner with serve,
// refuses it a second process and holds limits. The runner's output and
// errors go to stderr.
func StartRunner(spec Spec, args, env []string, stderr *os.File, limits Limits) (*Runner, error) {
	if st := RunnerAvailable(); !st.Confined {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, st.Reason)
	}
	if len(args) == 0 {
		return nil, errors.New("no runner program")
	}
	name, err := profileName(os.Getpid())
	if err != nil {
		return nil, err
	}
	sid, err := createProfile(name)
	if err != nil {
		return nil, fmt.Errorf("make the runner's app container: %w", err)
	}
	var granted []string
	release := func() {
		grantMu.Lock()
		for _, p := range granted {
			_ = revoke(p, sid)
		}
		grantMu.Unlock()
		_ = deleteProfile(name)
	}
	fail := func(err error) (*Runner, error) {
		release()
		return nil, err
	}
	for _, dir := range spec.ReadWrite {
		if err := dropLinks(dir); err != nil {
			return fail(fmt.Errorf("check the runner's folder %s: %w", dir, err))
		}
	}
	grantMu.Lock()
	for _, g := range []struct {
		paths   []string
		access  windows.ACCESS_MASK
		inherit uint32
		must    bool
	}{
		{spec.ReadWrite, modifyAccess, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, true},
		{spec.Read, readAccess, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, true},
		{socketFolders(spec.Sockets), linkAccess, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, true},
		{spec.Exec, execAccess, windows.NO_INHERITANCE, false},
	} {
		for _, p := range g.paths {
			err := grant(p, sid, g.access, g.inherit)
			if err == nil {
				granted = append(granted, p)
				continue
			}
			if g.must || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				grantMu.Unlock()
				return fail(fmt.Errorf("give the runner %s: %w", p, err))
			}
		}
	}
	grantMu.Unlock()

	job, err := runnerJob(limits)
	if err != nil {
		return fail(fmt.Errorf("make the runner's job: %w", err))
	}
	pi, err := createInContainer(args, env, stderr, sid)
	if err != nil {
		_ = windows.CloseHandle(job)
		return fail(fmt.Errorf("start the runner in its app container: %w", err))
	}
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		return fail(fmt.Errorf("put the runner in its job: %w", err))
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		return fail(fmt.Errorf("resume the runner: %w", err))
	}
	_ = windows.CloseHandle(pi.Thread)
	return &Runner{pid: int(pi.ProcessId), process: pi.Process, job: job, release: release}, nil
}

// socketFolders are the folders of the sockets. A socket file is a reparse
// point, so the grant goes on its folder, which serve makes for one runner.
func socketFolders(sockets []string) []string {
	var out []string
	for _, s := range sockets {
		if dir := filepath.Dir(s); !slices.Contains(out, dir) {
			out = append(out, dir)
		}
	}
	return out
}

// dropLinks removes each junction and symbolic link under dir, so that the
// access granted on dir reaches no folder outside it.
func dropLinks(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
		if !ok || attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
			return nil
		}
		if p == dir {
			return fmt.Errorf("%s is a link", dir)
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}

func profileName(servePID int) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return profilePrefix + strconv.Itoa(servePID) + "." + hex.EncodeToString(b), nil
}

func createProfile(name string) (*windows.SID, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var raw *windows.SID
	if r, _, _ := procCreateProfile.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(p)), 0, 0, uintptr(unsafe.Pointer(&raw))); r != 0 {
		return nil, fmt.Errorf("CreateAppContainerProfile: HRESULT 0x%08x", uint32(r))
	}
	defer windows.FreeSid(raw)
	sid, err := raw.Copy()
	if err != nil {
		_ = deleteProfile(name)
		return nil, err
	}
	return sid, nil
}

func deleteProfile(name string) error {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	if r, _, _ := procDeleteProfile.Call(uintptr(unsafe.Pointer(p))); r != 0 {
		return fmt.Errorf("DeleteAppContainerProfile: HRESULT 0x%08x", uint32(r))
	}
	return nil
}

func grant(path string, sid *windows.SID, access windows.ACCESS_MASK, inherit uint32) error {
	return editList(path, windows.EXPLICIT_ACCESS{AccessPermissions: access, AccessMode: windows.GRANT_ACCESS, Inheritance: inherit,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}})
}

func revoke(path string, sid *windows.SID) error {
	err := editList(path, windows.EXPLICIT_ACCESS{AccessMode: windows.REVOKE_ACCESS,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}})
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return nil
	}
	return err
}

func editList(path string, entry windows.EXPLICIT_ACCESS) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, old)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

const uiRestrictions = windows.JOB_OBJECT_UILIMIT_DESKTOP | windows.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS | windows.JOB_OBJECT_UILIMIT_EXITWINDOWS |
	windows.JOB_OBJECT_UILIMIT_GLOBALATOMS | windows.JOB_OBJECT_UILIMIT_HANDLES | windows.JOB_OBJECT_UILIMIT_READCLIPBOARD |
	windows.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS | windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD

func runnerJob(l Limits) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
		windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	info.BasicLimitInformation.ActiveProcessLimit = 1
	if l.CPUSeconds > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_TIME
		info.BasicLimitInformation.PerProcessUserTimeLimit = int64(l.CPUSeconds) * 10_000_000
	}
	if l.Memory > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
		info.ProcessMemoryLimit = uintptr(l.Memory)
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	ui := windows.JOBOBJECT_BASIC_UI_RESTRICTIONS{UIRestrictionsClass: uiRestrictions}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

type securityCapabilities struct {
	AppContainerSid *windows.SID
	Capabilities    uintptr
	CapabilityCount uint32
	Reserved        uint32
}

// createInContainer starts args suspended in the app container of sid. The
// runner inherits one handle, stderr, as its output and errors.
func createInContainer(args, env []string, stderr *os.File, sid *windows.SID) (*windows.ProcessInformation, error) {
	self := windows.CurrentProcess()
	var out windows.Handle
	if err := windows.DuplicateHandle(self, windows.Handle(stderr.Fd()), self, &out, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(out)
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	caps := securityCapabilities{AppContainerSid: sid}
	if err := attrs.Update(attrSecurityCapabilities, unsafe.Pointer(&caps), unsafe.Sizeof(caps)); err != nil {
		return nil, err
	}
	handles := []windows.Handle{out}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return nil, err
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdOutput, si.StdErr = out, out
	exe, err := windows.UTF16PtrFromString(args[0])
	if err != nil {
		return nil, err
	}
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return nil, err
	}
	block, err := envBlock(env)
	if err != nil {
		return nil, err
	}
	pi := new(windows.ProcessInformation)
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(exe, cmdline, nil, nil, true, flags, &block[0], nil, &si.StartupInfo, pi); err != nil {
		return nil, err
	}
	return pi, nil
}

// envBlock is env as a sorted Unicode environment block with SYSTEMROOT,
// which Windows sockets need.
func envBlock(env []string) ([]uint16, error) {
	env = slices.Clone(env)
	if !slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(strings.ToUpper(e), "SYSTEMROOT=") }) {
		if root, ok := os.LookupEnv("SYSTEMROOT"); ok {
			env = append(env, "SYSTEMROOT="+root)
		}
	}
	slices.SortFunc(env, func(a, b string) int { return strings.Compare(strings.ToUpper(a), strings.ToUpper(b)) })
	var block []uint16
	for _, e := range env {
		if strings.ContainsRune(e, 0) {
			return nil, fmt.Errorf("the runner's environment holds a NUL")
		}
		block = append(block, utf16(e)...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0), nil
}

func utf16(s string) []uint16 {
	u, _ := windows.UTF16FromString(s)
	return u[:len(u)-1]
}

// RemoveStaleRunners deletes the app container profiles of runners whose
// serve process has ended. It returns how many it deleted.
func RemoveStaleRunners() (int, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, profilesKey, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	sids, err := k.ReadSubKeyNames(-1)
	k.Close()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, sid := range sids {
		sk, err := registry.OpenKey(registry.CURRENT_USER, profilesKey+`\`+sid, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		moniker, _, err := sk.GetStringValue("Moniker")
		sk.Close()
		if err != nil || !strings.HasPrefix(strings.ToLower(moniker), strings.ToLower(profilePrefix)) {
			continue
		}
		pidText, _, _ := strings.Cut(moniker[len(profilePrefix):], ".")
		pid, err := strconv.Atoi(pidText)
		if err != nil || processAlive(pid) {
			continue
		}
		if deleteProfile(moniker) == nil {
			removed++
		}
	}
	return removed, nil
}

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}
