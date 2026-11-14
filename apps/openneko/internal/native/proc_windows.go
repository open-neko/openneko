package native

import (
	"errors"
	"os"
	"syscall"
)

var errWindows = errors.New("the native runtime does not run on Windows yet")

func Lock(Layout) (func(), error)     { return nil, errWindows }
func Locked(Layout) bool              { return false }
func Alive(int) bool                  { return false }
func terminate(p *os.Process) error   { return p.Kill() }
func childAttr() *syscall.SysProcAttr { return nil }
func Detach() *syscall.SysProcAttr    { return nil }
