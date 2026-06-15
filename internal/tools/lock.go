package tools

import (
	"path/filepath"
	"sort"
	"sync"
)

// FileLockManager manages shared read and exclusive write locks for files
// to prevent concurrent modification contention and dirty reads.
type FileLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

// GlobalLockManager is the process-wide lock manager instance.
var GlobalLockManager = &FileLockManager{
	locks: make(map[string]*sync.RWMutex),
}

func (m *FileLockManager) getLock(path string) *sync.RWMutex {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[abs]
	if !ok {
		l = &sync.RWMutex{}
		m.locks[abs] = l
	}
	return l
}

// Lock acquires an exclusive write lock on the specified file path.
func (m *FileLockManager) Lock(path string) {
	m.getLock(path).Lock()
}

// Unlock releases the exclusive write lock on the specified file path.
func (m *FileLockManager) Unlock(path string) {
	m.getLock(path).Unlock()
}

// RLock acquires a shared read lock on the specified file path.
func (m *FileLockManager) RLock(path string) {
	m.getLock(path).RLock()
}

// RUnlock releases the shared read lock on the specified file path.
func (m *FileLockManager) RUnlock(path string) {
	m.getLock(path).RUnlock()
}

// LockMany acquires exclusive write locks on multiple file paths in sorted
// alphabetical order to prevent deadlocks, returning a function that releases
// all locks in reverse order.
func (m *FileLockManager) LockMany(paths []string) func() {
	if len(paths) == 0 {
		return func() {}
	}
	unique := make(map[string]struct{})
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		unique[abs] = struct{}{}
	}
	sorted := make([]string, 0, len(unique))
	for abs := range unique {
		sorted = append(sorted, abs)
	}
	sort.Strings(sorted)

	for _, abs := range sorted {
		m.Lock(abs)
	}

	return func() {
		for i := len(sorted) - 1; i >= 0; i-- {
			m.Unlock(sorted[i])
		}
	}
}
