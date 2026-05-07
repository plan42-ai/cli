package githubevents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/plan42-ai/concurrency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testConn1 = "conn1"
const testOrg1 = "org1"

// newTestStore creates a CheckpointStore backed by a temp directory.
// The returned cleanup function stops the timer goroutine.
func newTestStore(t *testing.T) (*CheckpointStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)
	cg := concurrency.NewContextGroup()
	t.Cleanup(func() {
		cg.Cancel()
		_ = cg.WaitTimeout(5 * time.Second)
	})

	store, err := newCheckpointStoreFromPath(path, cg)
	require.NoError(t, err)
	return store, path
}

// newTestStoreWithFile writes the given JSON data to the checkpoint file
// before constructing the store, simulating a pre-existing file on startup.
func newTestStoreWithFile(t *testing.T, data []byte) *CheckpointStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)
	require.NoError(t, os.WriteFile(path, data, 0600))

	cg := concurrency.NewContextGroup()
	t.Cleanup(func() {
		cg.Cancel()
		_ = cg.WaitTimeout(5 * time.Second)
	})

	store, err := newCheckpointStoreFromPath(path, cg)
	require.NoError(t, err)
	return store
}

func TestLoadFromMissingFile(t *testing.T) {
	store, _ := newTestStore(t)

	key := CheckpointKey{GithubConnectionID: testConn1, OrgName: testOrg1}
	_, ok := store.Get(key)
	assert.False(t, ok, "missing file should yield empty state")
}

func TestLoadFromValidFile(t *testing.T) {
	fileData := `{
		"conn-abc:my-org": {
			"last_event_id": "12345",
			"etag": "\"abc\"",
			"poll_interval_seconds": 90
		}
	}`
	store := newTestStoreWithFile(t, []byte(fileData))

	key := CheckpointKey{GithubConnectionID: "conn-abc", OrgName: "my-org"}
	cp, ok := store.Get(key)
	require.True(t, ok)
	assert.Equal(t, "12345", cp.LastEventID)
	assert.Equal(t, `"abc"`, cp.ETag)
	assert.Equal(t, 90, cp.PollIntervalSecs)
}

func TestLoadFromCorruptFile(t *testing.T) {
	// Corrupt JSON should not prevent store creation.
	store := newTestStoreWithFile(t, []byte(`not valid json at all!!!`))

	key := CheckpointKey{GithubConnectionID: "x", OrgName: "y"}
	_, ok := store.Get(key)
	assert.False(t, ok, "corrupt file should yield empty state")
}

func TestSetGetDelete(t *testing.T) {
	store, _ := newTestStore(t)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}

	// Initially absent.
	_, ok := store.Get(key)
	assert.False(t, ok)

	// Set and retrieve.
	store.Set(key, Checkpoint{LastEventID: "100", ETag: `"e"`, PollIntervalSecs: 60})
	cp, ok := store.Get(key)
	require.True(t, ok)
	assert.Equal(t, "100", cp.LastEventID)
	assert.Equal(t, `"e"`, cp.ETag)
	assert.Equal(t, 60, cp.PollIntervalSecs)

	// Overwrite.
	store.Set(key, Checkpoint{LastEventID: "200", ETag: `"f"`, PollIntervalSecs: 120})
	cp, ok = store.Get(key)
	require.True(t, ok)
	assert.Equal(t, "200", cp.LastEventID)

	// Delete.
	store.Delete(key)
	_, ok = store.Get(key)
	assert.False(t, ok)
}

func TestDirtyFlag(t *testing.T) {
	store, _ := newTestStore(t)

	store.mu.Lock()
	assert.False(t, store.dirty, "new store should not be dirty")
	store.mu.Unlock()

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "1"})

	store.mu.Lock()
	assert.True(t, store.dirty, "store should be dirty after Set")
	store.mu.Unlock()
}

func TestTimerScheduledOnFirstMutation(t *testing.T) {
	store, _ := newTestStore(t)

	store.mu.Lock()
	assert.True(t, store.stopped, "timer should start stopped")
	store.mu.Unlock()

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "1"})

	store.mu.Lock()
	assert.False(t, store.stopped, "timer should be running after mutation")
	store.mu.Unlock()
}

func TestTimerNotResetOnSubsequentMutations(t *testing.T) {
	store, _ := newTestStore(t)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}

	store.Set(key, Checkpoint{LastEventID: "1"})

	store.mu.Lock()
	assert.False(t, store.stopped)
	store.mu.Unlock()

	// Second mutation should leave the timer running (stopped stays false).
	store.Set(key, Checkpoint{LastEventID: "2"})

	store.mu.Lock()
	assert.False(t, store.stopped, "timer should still be running")
	store.mu.Unlock()
}

func TestFlushWritesToDisk(t *testing.T) {
	store, path := newTestStore(t)
	key := CheckpointKey{GithubConnectionID: testConn1, OrgName: testOrg1}
	store.Set(key, Checkpoint{LastEventID: "42", ETag: `"etag1"`, PollIntervalSecs: 60})

	// Directly call flush to write immediately.
	store.flush()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var parsed map[string]checkpointFileEntry
	require.NoError(t, json.Unmarshal(data, &parsed))

	entry, ok := parsed[testConn1+":"+testOrg1]
	require.True(t, ok)
	assert.Equal(t, "42", entry.LastEventID)
	assert.Equal(t, `"etag1"`, entry.ETag)
	assert.Equal(t, 60, entry.PollIntervalSecs)
}

func TestFlushClearsDirtyFlag(t *testing.T) {
	store, _ := newTestStore(t)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "1"})

	store.flush()

	store.mu.Lock()
	assert.False(t, store.dirty, "flush should clear dirty flag")
	store.mu.Unlock()
}

func TestFlushNoOpWhenNotDirty(t *testing.T) {
	store, path := newTestStore(t)

	// Flush on a clean store should not create a file.
	store.flush()

	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "no file should be written when store is not dirty")
}

func TestAtomicWriteProducesCorrectFile(t *testing.T) {
	store, path := newTestStore(t)

	key1 := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	key2 := CheckpointKey{GithubConnectionID: "c2", OrgName: "o2"}
	store.Set(key1, Checkpoint{LastEventID: "10", ETag: `"e1"`, PollIntervalSecs: 60})
	store.Set(key2, Checkpoint{LastEventID: "20", ETag: `"e2"`, PollIntervalSecs: 120})

	store.flush()

	// Read the file back and verify all entries.
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var parsed map[string]checkpointFileEntry
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Len(t, parsed, 2)

	e1 := parsed["c1:o1"]
	assert.Equal(t, "10", e1.LastEventID)
	assert.Equal(t, `"e1"`, e1.ETag)
	assert.Equal(t, 60, e1.PollIntervalSecs)

	e2 := parsed["c2:o2"]
	assert.Equal(t, "20", e2.LastEventID)
	assert.Equal(t, `"e2"`, e2.ETag)
	assert.Equal(t, 120, e2.PollIntervalSecs)
}

func TestShutdownFlushWritesPendingChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)
	cg := concurrency.NewContextGroup()

	store, err := newCheckpointStoreFromPath(path, cg)
	require.NoError(t, err)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "99", ETag: `"final"`, PollIntervalSecs: 60})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Flush now cancels its own cg internally, so the watchTimer goroutine
	// exits and WaitContext returns promptly.
	err = store.Flush(ctx)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var parsed map[string]checkpointFileEntry
	require.NoError(t, json.Unmarshal(data, &parsed))

	entry, ok := parsed["c1:o1"]
	require.True(t, ok)
	assert.Equal(t, "99", entry.LastEventID)
	assert.Equal(t, `"final"`, entry.ETag)
}

func TestFilePermissions(t *testing.T) {
	store, path := newTestStore(t)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "1"})

	store.flush()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"checkpoint file should have 0600 permissions")
}

func TestDirectoryCreation(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "home", configDirName)
	path := filepath.Join(configDir, checkpointFileName)

	// The config dir doesn't exist yet. newCheckpointStoreFromPath doesn't
	// create it (that's NewCheckpointStore's job), but we can verify the
	// NewCheckpointStore path by setting HOME.
	t.Setenv("HOME", filepath.Join(dir, "home"))

	cg := concurrency.NewContextGroup()
	t.Cleanup(func() {
		cg.Cancel()
		_ = cg.WaitTimeout(5 * time.Second)
	})

	store, err := NewCheckpointStore(cg)
	require.NoError(t, err)

	// Verify the config directory was created with correct permissions.
	info, err := os.Stat(configDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm(),
		"config directory should have 0700 permissions")

	// Verify the path matches expectation.
	assert.Equal(t, path, store.path)
}

func TestConcurrentAccess(t *testing.T) {
	store, _ := newTestStore(t)

	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			key := CheckpointKey{
				GithubConnectionID: "conn",
				OrgName:            "org",
			}
			for i := range iterations {
				switch i % 3 {
				case 0:
					store.Set(key, Checkpoint{
						LastEventID:      string(rune('A' + id%26)),
						PollIntervalSecs: id,
					})
				case 1:
					store.Get(key)
				default:
					store.Delete(key)
				}
			}
		}(g)
	}

	wg.Wait()
	// If we get here without a race, the test passes. Flush to verify
	// the store is still in a consistent state.
	store.flush()
}

func TestDebouncedFlushViaTimer(t *testing.T) {
	// Create a store that uses a very short debounce to test the timer path.
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)

	cg := concurrency.NewContextGroup()
	t.Cleanup(func() {
		cg.Cancel()
		_ = cg.WaitTimeout(5 * time.Second)
	})

	store, err := newCheckpointStoreFromPath(path, cg)
	require.NoError(t, err)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "timer-test"})

	// Override the timer to fire almost immediately.
	store.mu.Lock()
	store.timer.Reset(10 * time.Millisecond)
	store.mu.Unlock()

	// Wait for the file to appear.
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var parsed map[string]checkpointFileEntry
		if json.Unmarshal(data, &parsed) != nil {
			return false
		}
		entry, ok := parsed["c1:o1"]
		return ok && entry.LastEventID == "timer-test"
	}, 2*time.Second, 10*time.Millisecond, "timer-driven flush should write the file")
}

func TestRoundTripThroughFile(t *testing.T) {
	// Write some data, flush, then create a new store from the same file.
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)

	cg1 := concurrency.NewContextGroup()
	store1, err := newCheckpointStoreFromPath(path, cg1)
	require.NoError(t, err)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store1.Set(key, Checkpoint{
		LastEventID:      "round-trip",
		ETag:             `"rt-etag"`,
		PollIntervalSecs: 90,
	})
	store1.flush()

	cg1.Cancel()
	_ = cg1.WaitTimeout(2 * time.Second)

	// Second store from the same file.
	cg2 := concurrency.NewContextGroup()
	t.Cleanup(func() {
		cg2.Cancel()
		_ = cg2.WaitTimeout(5 * time.Second)
	})

	store2, err := newCheckpointStoreFromPath(path, cg2)
	require.NoError(t, err)

	cp, ok := store2.Get(key)
	require.True(t, ok)
	assert.Equal(t, "round-trip", cp.LastEventID)
	assert.Equal(t, `"rt-etag"`, cp.ETag)
	assert.Equal(t, 90, cp.PollIntervalSecs)
}

func TestDeleteRemovesEntryFromDisk(t *testing.T) {
	store, path := newTestStore(t)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "1"})
	store.flush()

	store.Delete(key)
	store.flush()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var parsed map[string]checkpointFileEntry
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Empty(t, parsed)
}

func TestParseCompositeKey(t *testing.T) {
	tests := []struct {
		input   string
		wantKey CheckpointKey
		wantOk  bool
	}{
		{"conn:org", CheckpointKey{"conn", "org"}, true},
		{"a:b:c", CheckpointKey{"a", "b:c"}, true}, // colon in org name
		{"nocolon", CheckpointKey{}, false},
		{":empty-conn", CheckpointKey{"", "empty-conn"}, true},
		{"empty-org:", CheckpointKey{"empty-org", ""}, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			key, ok := parseCompositeKey(tt.input)
			assert.Equal(t, tt.wantOk, ok)
			if ok {
				assert.Equal(t, tt.wantKey, key)
			}
		})
	}
}

func TestMultipleEntries(t *testing.T) {
	fileData := `{
		"conn1:org1": {
			"last_event_id": "100",
			"etag": "\"e1\"",
			"poll_interval_seconds": 60
		},
		"conn2:org2": {
			"last_event_id": "200",
			"etag": "\"e2\"",
			"poll_interval_seconds": 120
		}
	}`
	store := newTestStoreWithFile(t, []byte(fileData))

	cp1, ok := store.Get(CheckpointKey{GithubConnectionID: testConn1, OrgName: testOrg1})
	require.True(t, ok)
	assert.Equal(t, "100", cp1.LastEventID)
	assert.Equal(t, 60, cp1.PollIntervalSecs)

	cp2, ok := store.Get(CheckpointKey{GithubConnectionID: "conn2", OrgName: "org2"})
	require.True(t, ok)
	assert.Equal(t, "200", cp2.LastEventID)
	assert.Equal(t, 120, cp2.PollIntervalSecs)
}
