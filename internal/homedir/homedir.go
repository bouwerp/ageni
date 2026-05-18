// Package homedir provides a cached, timeout-protected wrapper around
// os.UserHomeDir. On systems where home directory lookup is slow or blocked
// (LDAP/NFS home directories, enterprise authentication), os.UserHomeDir()
// can block indefinitely. This package resolves it once at first use with a
// 2-second deadline and caches the result for all subsequent calls.
package homedir

import (
	"os"
	"sync"
	"time"
)

var (
	once      sync.Once
	cachedDir string
	cachedErr error
)

// Dir returns the current user's home directory. It calls os.UserHomeDir()
// with a 2-second timeout on the first invocation and caches the result.
// If the lookup times out or fails, Dir returns ("", err).
func Dir() (string, error) {
	once.Do(func() {
		type result struct {
			dir string
			err error
		}
		ch := make(chan result, 1)
		go func() {
			d, e := os.UserHomeDir()
			ch <- result{d, e}
		}()
		select {
		case r := <-ch:
			cachedDir, cachedErr = r.dir, r.err
		case <-time.After(2 * time.Second):
			cachedErr = os.ErrDeadlineExceeded
		}
	})
	return cachedDir, cachedErr
}
