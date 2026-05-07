package githubevents

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/renameio/v2"
	"github.com/plan42-ai/concurrency"
)

const (
	flushDebounce      = 5 * time.Minute
	checkpointFileName = "plan42-runner.checkpoint.json"
	configDirName      = ".config"
	configDirPerm      = 0700
	checkpointFilePerm = 0600
)

// CheckpointKey identifies a unique polling pair.
type CheckpointKey struct {
	GithubConnectionID string
	OrgName            string
}

// ConnectionInfo carries the connection details needed to construct a
// GitHub client for polling a (GithubConnectionID, OrgName) pair.
type ConnectionInfo struct {
	Token   string
	BaseURL string
	User    string
}

// Checkpoint holds the durable state for a single polling pair.
type Checkpoint struct {
	LastEventID      string
	ETag             string
	PollIntervalSecs int
}

// checkpointFileEntry is the JSON-serializable form of a single checkpoint.
type checkpointFileEntry struct {
	LastEventID      string `json:"last_event_id"`
	ETag             string `json:"etag"`
	PollIntervalSecs int    `json:"poll_interval_seconds"`
}

// CheckpointStore persists polling checkpoints to disk so a runner restart
// doesn't replay the entire Events API window. Writes are debounced: the
// in-memory map is the canonical state, and a timer-driven flush writes it
// to disk at most once per flushDebounce interval.
type CheckpointStore struct {
	mu      sync.Mutex
	entries map[CheckpointKey]Checkpoint
	dirty   bool
	timer   *time.Timer
	stopped bool
	path    string
	cg      *concurrency.ContextGroup
}

// NewCheckpointStore creates a CheckpointStore, loading any existing checkpoint
// file from ~/.config/plan42-runner.checkpoint.json. A missing file is treated
// as empty. An unparseable file is logged and treated as empty.
func NewCheckpointStore(cg *concurrency.ContextGroup) (*CheckpointStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configDir := filepath.Join(home, configDirName)
	if err := os.MkdirAll(configDir, configDirPerm); err != nil {
		return nil, err
	}

	path := filepath.Join(configDir, checkpointFileName)
	return newCheckpointStoreFromPath(path, cg)
}

// newCheckpointStoreFromPath is the internal constructor that accepts an
// explicit file path. The exported constructor computes the path from the
// user's home directory.
func newCheckpointStoreFromPath(path string, cg *concurrency.ContextGroup) (*CheckpointStore, error) {
	entries := make(map[CheckpointKey]Checkpoint)
	data, err := os.ReadFile(path)
	if err == nil {
		if parseErr := parseCheckpointFile(data, entries); parseErr != nil {
			slog.Error("failed to parse checkpoint file; starting with empty state", "path", path, "error", parseErr)
		}
	} else if !os.IsNotExist(err) {
		slog.Error("failed to read checkpoint file; starting with empty state", "path", path, "error", err)
	}

	// Create a stopped timer: fire far in the future, then immediately stop.
	timer := time.NewTimer(time.Duration(math.MaxInt64))
	timer.Stop()

	s := &CheckpointStore{
		entries: entries,
		timer:   timer,
		stopped: true,
		path:    path,
		cg:      cg,
	}

	cg.Add(1)
	go s.watchTimer()

	return s, nil
}

// parseCheckpointFile parses the JSON checkpoint data into the entries map.
func parseCheckpointFile(data []byte, entries map[CheckpointKey]Checkpoint) error {
	var raw map[string]checkpointFileEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for compositeKey, entry := range raw {
		key, ok := parseCompositeKey(compositeKey)
		if !ok {
			continue
		}
		entries[key] = Checkpoint(entry)
	}
	return nil
}

// parseCompositeKey splits "GithubConnectionID:OrgName" into a CheckpointKey.
func parseCompositeKey(s string) (CheckpointKey, bool) {
	for i := range s {
		if s[i] == ':' {
			return CheckpointKey{
				GithubConnectionID: s[:i],
				OrgName:            s[i+1:],
			}, true
		}
	}
	return CheckpointKey{}, false
}

// compositeKey returns the stable file key "GithubConnectionID:OrgName".
func compositeKey(k CheckpointKey) string {
	return k.GithubConnectionID + ":" + k.OrgName
}

// Get returns the checkpoint for the given key.
func (s *CheckpointStore) Get(key CheckpointKey) (Checkpoint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.entries[key]
	return cp, ok
}

// Set updates the checkpoint for the given key and marks the store dirty.
func (s *CheckpointStore) Set(key CheckpointKey, cp Checkpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = cp
	s.markDirty()
}

// Delete removes the checkpoint for the given key and marks the store dirty.
func (s *CheckpointStore) Delete(key CheckpointKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	s.markDirty()
}

// markDirty sets the dirty flag and schedules the timer if it is stopped.
// Must be called with s.mu held.
func (s *CheckpointStore) markDirty() {
	s.dirty = true
	if s.stopped {
		s.timer.Reset(flushDebounce)
		s.stopped = false
	}
}

// watchTimer waits for the debounce timer to fire and schedules a flush.
func (s *CheckpointStore) watchTimer() {
	defer s.cg.Done()
	ctx := s.cg.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.timer.C:
			s.cg.Add(1)
			go func() {
				defer s.cg.Done()
				s.flush()
			}()
		}
	}
}

// flush writes the in-memory checkpoint map to disk atomically.
func (s *CheckpointStore) flush() {
	s.mu.Lock()
	s.stopped = true
	if !s.dirty {
		s.mu.Unlock()
		return
	}

	snapshot := s.snapshotLocked()
	s.dirty = false
	s.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		slog.Error("failed to marshal checkpoint data", "error", err)
		s.retryAfterFailure()
		return
	}

	if err := renameio.WriteFile(s.path, data, checkpointFilePerm); err != nil {
		slog.Error("failed to write checkpoint file", "path", s.path, "error", err)
		s.retryAfterFailure()
		return
	}
}

// retryAfterFailure re-marks the store dirty and reschedules the timer.
func (s *CheckpointStore) retryAfterFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = true
	s.timer.Reset(flushDebounce)
	s.stopped = false
}

// snapshotLocked returns a serializable copy of the entries map.
// Must be called with s.mu held.
func (s *CheckpointStore) snapshotLocked() map[string]checkpointFileEntry {
	out := make(map[string]checkpointFileEntry, len(s.entries))
	for k, v := range s.entries {
		out[compositeKey(k)] = checkpointFileEntry(v)
	}
	return out
}

// Flush is called during runner shutdown. It cancels the watch goroutine,
// stops the debounce timer, waits for any in-flight timer-scheduled flush,
// and runs a synchronous final flush.
func (s *CheckpointStore) Flush(ctx context.Context) error {
	s.mu.Lock()
	s.timer.Stop()
	s.stopped = true
	s.mu.Unlock()

	// Cancel the cg so the watchTimer goroutine exits, then wait for
	// any in-flight flush to complete.
	s.cg.Cancel()
	if err := s.cg.WaitContext(ctx); err != nil {
		return err
	}

	s.flush()
	return nil
}
