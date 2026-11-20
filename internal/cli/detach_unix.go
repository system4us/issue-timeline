//go:build unix

package cli

import "syscall"

// detachAttr: the child starts a new session, so closing the terminal does not reach it.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
