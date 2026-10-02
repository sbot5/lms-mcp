//go:build !windows

package secrets

// Open returns the environment-backed store on non-Windows platforms,
// including cloud development sessions. target is ignored here.
func Open(target string) (Store, error) {
	return envStore{}, nil
}
