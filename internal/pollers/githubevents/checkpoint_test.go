package githubevents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/plan42-ai/clock"
	"github.com/stretchr/testify/require"
)

const testConn1 = "conn1"
const testOrg1 = "org1"

// newTestStoreWithClock creates a CheckpointStore backed by a temp directory and
// the given clock, returning the store and its file path. Tests pass a fake
// clock to drive the debounce timer without waiting on wall-clock time.
func newTestStoreWithClock(t *testing.T, clk clock.Clock) (*CheckpointStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)
	store, err := NewCheckpointStore(path, clk)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.ShutdownTimeout(5 * time.Second)
	})
	return store, path
}

// waitForCheckpointFile waits until the checkpoint file at path records
// lastEventID for the given composite key. The flush runs asynchronously after
// the debounce timer fires, so the test polls (it does not wait wall-clock time
// for the debounce itself — a fake clock advances that instantly).
func waitForCheckpointFile(t *testing.T, path, compositeKey, lastEventID string) {
	t.Helper()
	// The flush runs in a goroutine after the (fake-clock) timer fires; give it a
	// brief moment to land, then verify the file directly.
	time.Sleep(100 * time.Millisecond)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var parsed map[string]checkpointFileEntry
	require.NoError(t, json.Unmarshal(data, &parsed))

	entry, ok := parsed[compositeKey]
	require.True(t, ok, "checkpoint file should record the key")
	require.Equal(t, lastEventID, entry.LastEventID)
}

// newTestStore creates a CheckpointStore backed by a temp directory.
// The returned cleanup function stops the timer goroutine.
func newTestStore(t *testing.T) (*CheckpointStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)
	store, err := NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.ShutdownTimeout(5 * time.Second)
	})
	return store, path
}

// newTestStoreWithFile writes the given JSON data to the checkpoint file
// before constructing the store, simulating a pre-existing file on startup.
func newTestStoreWithFile(t *testing.T, data []byte) *CheckpointStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)
	require.NoError(t, os.WriteFile(path, data, 0600))

	store, err := NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.ShutdownTimeout(5 * time.Second)
	})
	return store
}

func TestLoadFromMissingFile(t *testing.T) {
	store, _ := newTestStore(t)

	key := CheckpointKey{GithubConnectionID: testConn1, OrgName: testOrg1}
	_, ok := store.Get(key)
	require.False(t, ok, "missing file should yield empty state")
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
	require.Equal(t, "12345", cp.LastEventID)
	require.Equal(t, `"abc"`, cp.ETag)
	require.Equal(t, 90, cp.PollIntervalSecs)
}

func TestLoadFromCorruptFile(t *testing.T) {
	// Corrupt JSON should not prevent store creation.
	store := newTestStoreWithFile(t, []byte(`not valid json at all!!!`))

	key := CheckpointKey{GithubConnectionID: "x", OrgName: "y"}
	_, ok := store.Get(key)
	require.False(t, ok, "corrupt file should yield empty state")
}

func TestSetGetDelete(t *testing.T) {
	store, _ := newTestStore(t)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}

	// Initially absent.
	_, ok := store.Get(key)
	require.False(t, ok)

	// Set and retrieve.
	store.Set(key, Checkpoint{LastEventID: "100", ETag: `"e"`, PollIntervalSecs: 60})
	cp, ok := store.Get(key)
	require.True(t, ok)
	require.Equal(t, "100", cp.LastEventID)
	require.Equal(t, `"e"`, cp.ETag)
	require.Equal(t, 60, cp.PollIntervalSecs)

	// Overwrite.
	store.Set(key, Checkpoint{LastEventID: "200", ETag: `"f"`, PollIntervalSecs: 120})
	cp, ok = store.Get(key)
	require.True(t, ok)
	require.Equal(t, "200", cp.LastEventID)

	// Delete.
	store.Delete(key)
	_, ok = store.Get(key)
	require.False(t, ok)
}

func TestSubsequentMutationDoesNotPushDebounce(t *testing.T) {
	clk := clock.NewFakeClock(time.Now())
	store, path := newTestStoreWithClock(t, clk)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}

	store.Set(key, Checkpoint{LastEventID: "1"})

	// Advance partway, then mutate again. Debouncing must coalesce: the second
	// mutation must not push the deadline out.
	clk.Advance(flushDebounce / 2)
	store.Set(key, Checkpoint{LastEventID: "2"})
	require.NoFileExists(t, path, "no flush before the original deadline")

	// Reaching the original deadline flushes. If the second Set had reset the
	// timer, nothing would be written yet.
	clk.Advance(flushDebounce / 2)
	waitForCheckpointFile(t, path, "c1:o1", "2")
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
	require.Equal(t, "42", entry.LastEventID)
	require.Equal(t, `"etag1"`, entry.ETag)
	require.Equal(t, 60, entry.PollIntervalSecs)
}

func TestFlushIsNoOpAfterFlush(t *testing.T) {
	store, path := newTestStore(t)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "1"})

	store.flush()
	require.FileExists(t, path)

	// The flush cleared the dirty flag, so a second flush writes nothing.
	require.NoError(t, os.Remove(path))
	store.flush()
	require.NoFileExists(t, path, "flush should be a no-op when nothing changed")
}

func TestFlushNoOpWhenNotDirty(t *testing.T) {
	store, path := newTestStore(t)

	// Flush on a clean store should not create a file.
	store.flush()

	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "no file should be written when store is not dirty")
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
	require.Len(t, parsed, 2)

	e1 := parsed["c1:o1"]
	require.Equal(t, "10", e1.LastEventID)
	require.Equal(t, `"e1"`, e1.ETag)
	require.Equal(t, 60, e1.PollIntervalSecs)

	e2 := parsed["c2:o2"]
	require.Equal(t, "20", e2.LastEventID)
	require.Equal(t, `"e2"`, e2.ETag)
	require.Equal(t, 120, e2.PollIntervalSecs)
}

func TestShutdownFlushWritesPendingChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)

	store, err := NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "99", ETag: `"final"`, PollIntervalSecs: 60})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = store.Shutdown(ctx)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var parsed map[string]checkpointFileEntry
	require.NoError(t, json.Unmarshal(data, &parsed))

	entry, ok := parsed["c1:o1"]
	require.True(t, ok)
	require.Equal(t, "99", entry.LastEventID)
	require.Equal(t, `"final"`, entry.ETag)
}

func TestFilePermissions(t *testing.T) {
	store, path := newTestStore(t)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "1"})

	store.flush()

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"checkpoint file should have 0600 permissions")
}

func TestDirectoryCreation(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "sub", configDirName)
	path := filepath.Join(configDir, checkpointFileName)

	// The parent directory doesn't exist yet; the constructor must create it.
	store, err := NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.ShutdownTimeout(5 * time.Second)
	})

	info, err := os.Stat(configDir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0700), info.Mode().Perm(),
		"config directory should have 0700 permissions")
}

func TestDefaultCheckpointPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := DefaultCheckpointPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, configDirName, checkpointFileName), path)
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
	_ = store.flush()
}

func TestDebouncedFlushViaTimer(t *testing.T) {
	clk := clock.NewFakeClock(time.Now())
	store, path := newTestStoreWithClock(t, clk)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store.Set(key, Checkpoint{LastEventID: "timer-test"})

	// Nothing is written before the debounce interval elapses.
	require.NoFileExists(t, path, "no flush before the debounce interval")

	// Advancing past the debounce interval triggers the timer-driven flush.
	clk.Advance(flushDebounce)
	waitForCheckpointFile(t, path, "c1:o1", "timer-test")
}

func TestRoundTripThroughFile(t *testing.T) {
	// Write some data, flush, then create a new store from the same file.
	dir := t.TempDir()
	path := filepath.Join(dir, checkpointFileName)

	store1, err := NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: "o1"}
	store1.Set(key, Checkpoint{
		LastEventID:      "round-trip",
		ETag:             `"rt-etag"`,
		PollIntervalSecs: 90,
	})
	require.NoError(t, store1.flush())
	_ = store1.ShutdownTimeout(2 * time.Second)

	store2, err := NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store2.ShutdownTimeout(5 * time.Second)
	})

	cp, ok := store2.Get(key)
	require.True(t, ok)
	require.Equal(t, "round-trip", cp.LastEventID)
	require.Equal(t, `"rt-etag"`, cp.ETag)
	require.Equal(t, 90, cp.PollIntervalSecs)
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
	require.Empty(t, parsed)
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
			require.Equal(t, tt.wantOk, ok)
			if ok {
				require.Equal(t, tt.wantKey, key)
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
	require.Equal(t, "100", cp1.LastEventID)
	require.Equal(t, 60, cp1.PollIntervalSecs)

	cp2, ok := store.Get(CheckpointKey{GithubConnectionID: "conn2", OrgName: "org2"})
	require.True(t, ok)
	require.Equal(t, "200", cp2.LastEventID)
	require.Equal(t, 120, cp2.PollIntervalSecs)
}
