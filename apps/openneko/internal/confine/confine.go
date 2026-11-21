// Package confine limits the calling process, its threads and its children
// to the paths of a spec, for good. Linux uses Landlock and seccomp; macOS
// uses a Seatbelt profile.
package confine

import (
	"errors"
	"fmt"
)

// Spec names what a confined process keeps. Every other path is refused.
type Spec struct {
	// Read holds folders and files that it may read.
	Read []string
	// ReadWrite holds folders where it may read, write, create and remove.
	ReadWrite []string
	// Exec holds files that it may run.
	Exec []string
	// Sockets holds named local sockets that it may connect to.
	Sockets []string
	// Listen holds named local sockets that it may create, accept on and
	// connect to.
	Listen []string
	// NoTCP refuses every TCP bind and connect where Landlock can (ABI 4).
	NoTCP bool
}

// State tells how a process is confined.
type State struct {
	Mechanism string `json:"mechanism"`
	ABI       int    `json:"abi,omitempty"`
	Confined  bool   `json:"confined"`
	// Gaps names the controls that this kernel cannot give.
	Gaps []string `json:"gaps,omitempty"`
	// Reason tells why a process is not confined.
	Reason string `json:"reason,omitempty"`
}

func (s State) String() string {
	if !s.Confined {
		if s.Reason == "" {
			return "unconfined"
		}
		return "unconfined: " + s.Reason
	}
	out := s.Mechanism
	if s.ABI > 0 {
		out += fmt.Sprintf(" ABI %d", s.ABI)
	}
	return out
}

// NotAvailable is what the start, status, doctor and release-check say on a
// system where this build cannot confine an agent runner yet.
const NotAvailable = "agent confinement: not available on this system yet"

// ErrUnsupported wraps the error of a system or kernel that cannot confine.
var ErrUnsupported = errors.New("this system cannot confine openneko serve")
