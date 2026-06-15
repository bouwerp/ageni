package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileLockManager_RLock_WLock(t *testing.T) {
	manager := &FileLockManager{
		locks: make(map[string]*sync.RWMutex),
	}
	path := "test_file.txt"

	// Acquire shared read lock
	manager.RLock(path)

	// Acquire another shared read lock (should not block)
	readAcquired := make(chan bool, 1)
	go func() {
		manager.RLock(path)
		readAcquired <- true
		manager.RUnlock(path)
	}()

	select {
	case <-readAcquired:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RLock blocked concurrent RLock")
	}

	// Attempt write lock (should block)
	writeAcquired := make(chan bool, 1)
	go func() {
		manager.Lock(path)
		writeAcquired <- true
		manager.Unlock(path)
	}()

	select {
	case <-writeAcquired:
		t.Fatal("Lock acquired while RLock active")
	case <-time.After(100 * time.Millisecond):
		// success, write lock is blocked
	}

	// Release first read lock
	manager.RUnlock(path)

	// Now write lock should be acquirable
	select {
	case <-writeAcquired:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Lock failed to acquire after RUnlock")
	}
}

func TestFileLockManager_LockMany_DeadlockPrevention(t *testing.T) {
	manager := &FileLockManager{
		locks: make(map[string]*sync.RWMutex),
	}
	files := []string{"a.txt", "b.txt", "c.txt", "d.txt"}

	var wg sync.WaitGroup
	numWorkers := 50
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
			for step := 0; step < 20; step++ {
				// Randomly select a subset of files to lock
				k := r.Intn(len(files)) + 1
				perm := r.Perm(len(files))[:k]
				selected := make([]string, 0, k)
				for _, idx := range perm {
					selected = append(selected, files[idx])
				}

				unlock := manager.LockMany(selected)
				// Simulating some work
				time.Sleep(time.Duration(r.Intn(5)) * time.Millisecond)
				unlock()
			}
		}(i)
	}

	done := make(chan bool)
	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		// success, no deadlock
	case <-time.After(5 * time.Second):
		t.Fatal("Deadlock detected in LockMany!")
	}
}

func TestTools_ConcurrencyIntegrations(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.txt")
	fileB := filepath.Join(dir, "b.txt")

	// Pre-create fileA
	if err := os.WriteFile(fileA, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracker := NewChangeTracker(filepath.Join(dir, "changes.jsonl"), filepath.Join(dir, "snapshots"))

	rf := ReadFile{}
	wf := WriteFile{Tracker: tracker}
	ef := EditFile{Tracker: tracker}
	me := MultiEdit{Tracker: tracker}
	mv := MoveFile{Tracker: tracker}
	df := DeleteFile{Tracker: tracker}
	te := TransactionalEdit{Tracker: tracker}

	var wg sync.WaitGroup

	// Worker 1: Read and Write fileA
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			args, _ := json.Marshal(map[string]any{"path": fileA})
			_, _ = rf.Call(context.Background(), args)

			writeArgs, _ := json.Marshal(map[string]any{"path": fileA, "content": fmt.Sprintf("content %d", i)})
			_, _ = wf.Call(context.Background(), writeArgs)
		}
	}()

	// Worker 2: Move A to B and B to A
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			moveArgs1, _ := json.Marshal(map[string]any{"src": fileA, "dst": fileB, "overwrite": true})
			_, _ = mv.Call(context.Background(), moveArgs1)

			moveArgs2, _ := json.Marshal(map[string]any{"src": fileB, "dst": fileA, "overwrite": true})
			_, _ = mv.Call(context.Background(), moveArgs2)
		}
	}()

	// Worker 3: Edit fileA
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			// Write a known string to search/replace
			writeArgs, _ := json.Marshal(map[string]any{"path": fileA, "content": "original content"})
			_, _ = wf.Call(context.Background(), writeArgs)

			editArgs, _ := json.Marshal(map[string]any{"path": fileA, "old_string": "original content", "new_string": "modified content"})
			_, _ = ef.Call(context.Background(), editArgs)
		}
	}()

	// Worker 4: MultiEdit fileA
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			writeArgs, _ := json.Marshal(map[string]any{"path": fileA, "content": "first second"})
			_, _ = wf.Call(context.Background(), writeArgs)

			meArgs, _ := json.Marshal(map[string]any{
				"path": fileA,
				"edits": []map[string]any{
					{"old_string": "first", "new_string": "1st"},
					{"old_string": "second", "new_string": "2nd"},
				},
			})
			_, _ = me.Call(context.Background(), meArgs)
		}
	}()

	// Worker 5: Delete A and recreate it
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			delArgs, _ := json.Marshal(map[string]any{"path": fileA})
			_, _ = df.Call(context.Background(), delArgs)

			writeArgs, _ := json.Marshal(map[string]any{"path": fileA, "content": "recreated"})
			_, _ = wf.Call(context.Background(), writeArgs)
		}
	}()

	// Worker 6: TransactionalEdit on A and B
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			teArgs, _ := json.Marshal(map[string]any{
				"changes": []map[string]any{
					{"path": fileA, "content": fmt.Sprintf("txA %d", i)},
					{"path": fileB, "content": fmt.Sprintf("txB %d", i)},
				},
			})
			_, _ = te.Call(context.Background(), teArgs)
		}
	}()

	done := make(chan bool)
	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		// success
	case <-time.After(10 * time.Second):
		t.Fatal("Concurrency integrations test timed out (possible deadlock or race)")
	}
}
