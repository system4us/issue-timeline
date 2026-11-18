//go:build windows

package cli

import "syscall"

// DETACHED_PROCESS and CREATE_NEW_PROCESS_GROUP: the child has no console, so closing the terminal does not reach it.
const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup, HideWindow: true}
}
