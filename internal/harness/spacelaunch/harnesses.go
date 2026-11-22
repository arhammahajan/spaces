package spacelaunch

import (
	"fmt"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// printCache writes the inventory JSON to stdout.
func printCache(file CacheFile) int {
	data, err := marshalCache(file)
	if err != nil {
		return dief("%s", err)
	}
	fmt.Fprint(stdout, string(data))
	return ExitOK
}

// SpaceHarnesses implements `zj __space-harnesses`: print the cached
// agents+profiles inventory as JSON, rebuilding it first when missing or
// stale. The Spaces plugin reads this instead of shelling out to the
// slower per-source queries on every popup open.
func SpaceHarnesses(args []string) int {
	if len(args) > 0 {
		return dief("usage: zj __space-harnesses")
	}
	if !zellijBinAvailable() {
		return dief("zellij not found on PATH (set ZELLIJ_BIN)")
	}
	file, err := EnsureFresh(harnessenv.DefaultPaths())
	if err != nil {
		return dief("%s", err)
	}
	return printCache(file)
}
