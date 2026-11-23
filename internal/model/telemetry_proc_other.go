//go:build !linux

package model

import "errors"

func captureProcessSnapshot(_ bool) *procTableSnapshot {
	return newEmptyProcTableSnapshot()
}

func killOrphanPort(_ int, _ string) error {
	return errors.ErrUnsupported
}
