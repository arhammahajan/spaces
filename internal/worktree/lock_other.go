//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package worktree

func lockStoreFile(_ string, _ bool) (func(), error) {
	return func() {}, nil
}
