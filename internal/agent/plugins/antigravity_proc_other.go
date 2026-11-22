//go:build !linux

package plugins

func discoverAntigravitySessionFromProc(int) string {
	return ""
}
