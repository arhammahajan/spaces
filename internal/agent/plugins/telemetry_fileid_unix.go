//go:build unix

package plugins

import (
	"os"
	"syscall"
)

func telemetryFileIdentity(info os.FileInfo) (uint64, uint64) {
	if info == nil {
		return 0, 0
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return uint64(info.Size()), uint64(info.ModTime().UnixNano())
}
