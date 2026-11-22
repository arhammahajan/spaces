//go:build linux

package core

import (
	"net"
	"syscall"
)

// connUID returns the peer uid of an accepted unix socket via SO_PEERCRED,
// mirroring the Python handler's same-user check.
func connUID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var uid int
	var found bool
	var ctrlErr error
	ctrlErr = raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil {
			return
		}
		uid, found = int(cred.Uid), true
	})
	if ctrlErr != nil || !found {
		return 0, false
	}
	return uid, true
}
