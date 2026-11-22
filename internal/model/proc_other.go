//go:build !linux

package model

func procIsLive(int) bool                  { return false }
func detectDescendantAgent(int) string     { return "" }
func inspectPaneProcess(int) (string, int) { return "", 0 }
