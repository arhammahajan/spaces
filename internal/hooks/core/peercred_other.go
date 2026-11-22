//go:build !linux

package core

import "net"

// connUID is unavailable off Linux; the handler then skips the same-user
// check, mirroring the Python's except-pass around SO_PEERCRED.
func connUID(conn net.Conn) (int, bool) { return 0, false }
