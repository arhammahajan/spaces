// Package zjspace owns the Zellij Spaces plugin's server-side support
// commands plus the harnesses cache file they read:
//
//	__space-harnesses      print the cached agents+profiles inventory as JSON
//	__space-launch-agent   open an agent (optionally with a profile) in a new
//	                       tab of an explicit session, never attaching
//
// Unlike zjcli (stdlib-only by contract) this package may import the agents,
// profiles and harnessenv packages: profile resolution needs them. Unlike
// the interactive `zj agent` path, everything here takes an explicit session
// and never touches the client, because the plugin invokes these commands
// from background run_command tasks with no usable ambient ZELLIJ_*
// environment.
package spacelaunch
