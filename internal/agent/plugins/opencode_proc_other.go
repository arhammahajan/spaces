//go:build !linux

package plugins

import (
	"context"

	"github.com/arhammahajan/spaces/internal/agent"
)

func defaultPIDAlive(int) bool {
	return true
}

func discoverOpenCodeEndpoint(context.Context, agent.DaemonWatchTarget) (string, bool) {
	return "", false
}
