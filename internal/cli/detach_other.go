//go:build !unix && !windows

package cli

import "syscall"

// detachAttr: no detaching is known on this system; the child runs with the same terminal.
func detachAttr() *syscall.SysProcAttr { return nil }
