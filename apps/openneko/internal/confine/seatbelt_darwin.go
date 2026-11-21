package confine

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

//go:cgo_import_dynamic libc_dlsym dlsym "/usr/lib/libSystem.B.dylib"

var libc_dlsym_trampoline_addr uintptr

//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

// rtldDefault is RTLD_DEFAULT, (void *)-2.
const rtldDefault = ^uintptr(1)

// lookup finds a libSystem function at run time, so a macOS that drops it
// still starts openneko.
func lookup(name string) uintptr {
	b := append([]byte(name), 0)
	r, _, _ := syscall_syscall(libc_dlsym_trampoline_addr, rtldDefault, uintptr(unsafe.Pointer(&b[0])), 0)
	runtime.KeepAlive(b)
	return r
}

// sandboxInit applies a Seatbelt profile to the whole process for good.
func sandboxInit(profile string) error {
	fn := lookup("sandbox_init")
	if fn == 0 {
		return fmt.Errorf("%s has no sandbox_init", Kernel())
	}
	p := append([]byte(profile), 0)
	var errbuf *byte
	r, _, _ := syscall_syscall(fn, uintptr(unsafe.Pointer(&p[0])), 0, uintptr(unsafe.Pointer(&errbuf)))
	runtime.KeepAlive(p)
	if int32(r) == 0 {
		return nil
	}
	return fmt.Errorf("%s refused the agent runner's Seatbelt profile: %s", Kernel(), cString(errbuf))
}

func cString(p *byte) string {
	if p == nil {
		return "no reason given"
	}
	var out []byte
	for i := 0; i < 4096; i++ {
		c := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if c == 0 {
			break
		}
		out = append(out, c)
	}
	return string(out)
}
