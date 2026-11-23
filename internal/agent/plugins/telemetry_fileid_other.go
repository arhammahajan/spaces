//go:build !unix

package plugins

import "os"

func telemetryFileIdentity(info os.FileInfo) (uint64, uint64) {
	if info == nil {
		return 0, 0
	}
	return uint64(info.Size()), uint64(info.ModTime().UnixNano())
}
