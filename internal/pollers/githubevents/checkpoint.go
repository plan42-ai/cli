package githubevents

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/go-github/v81/github"
	"github.com/google/renameio/v2"
	"github.com/plan42-ai/clock"
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
	Token    string
	BaseURL  string
	User     string
	GHClient *github.Client
}

// Checkpoint holds the durable state for a single polling pair. The JSON tags
// define the on-disk file format.
type Checkpoint struct {
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
	clock   clock.Clock
	timer   clock.Timer
	stopped bool
	path    string
	cg      *concurrency.ContextGroup
}

// DefaultCheckpointPath returns the default checkpoint file path,
// ~/.config/plan42-runner.checkpoint.json.
func DefaultCheckpointPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDirName, checkpointFileName), nil
}

// NewCheckpointStore creates a CheckpointStore backed by the file at path, using
// clk for its debounce timer. The parent directory is created if needed. Any
// existing file is loaded; a missing or unparseable file is treated as empty.
// The debounce timer (and the goroutine that watches it) is created lazily on
// the first mutation.
func NewCheckpointStore(path string, clk clock.Clock) (*CheckpointStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), configDirPerm); err != nil {
		return nil, err
	}

	entries := make(map[CheckpointKey]Checkpoint)
	data, err := os.ReadFile(path)
	if err == nil {
		if parseErr := parseCheckpointFile(data, entries); parseErr != nil {
			slog.Error("failed to parse checkpoint file; starting with empty state", "path", path, "error", parseErr)
		}
	} else if !os.IsNotExist(err) {
		slog.Error("failed to read checkpoint file; starting with empty state", "path", path, "error", err)
	}

	return &CheckpointStore{
		entries: entries,
		clock:   clk,
		stopped: true,
		path:    path,
		cg:      concurrency.NewContextGroup(),
	}, nil
}

// parseCheckpointFile parses the JSON checkpoint data into the entries map.
func parseCheckpointFile(data []byte, entries map[CheckpointKey]Checkpoint) error {
	var raw map[string]Checkpoint
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for compositeKey, entry := range raw {
		key, ok := parseCompositeKey(compositeKey)
		if !ok {
			continue
		}
		entries[key] = entry
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

// markDirty sets the dirty flag and schedules a debounced flush if one isn't
// already pending. The timer (and its watcher goroutine) is created on the first
// scheduling and reused thereafter. Must be called with s.mu held.
func (s *CheckpointStore) markDirty() {
	s.dirty = true
	if !s.stopped {
		return // a flush is already scheduled; coalesce into it
	}
	s.stopped = false
	if s.timer == nil {
		s.timer = s.clock.NewTimer(flushDebounce)
		s.cg.Add(1)
		go s.watchTimer(s.timer)
		return
	}
	s.timer.Reset(flushDebounce)
}

// watchTimer waits for the debounce timer to fire and flushes. The flush runs
// inline on this single goroutine (not in a spawned one) so flushes are
// serialized: a later flush can never start while an earlier one is still
// writing, which would let the older write stomp the newer state on disk.
func (s *CheckpointStore) watchTimer(timer clock.Timer) {
	defer s.cg.Done()
	ctx := s.cg.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C():
			s.flushWithRetry()
		}
	}
}

// flush writes the in-memory checkpoint map to disk atomically. Returns an
// error if the snapshot is dirty and the write fails.
func (s *CheckpointStore) flush() error {
	snapshot, wasDirty := s.snapshot()
	if !wasDirty {
		return nil
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	return renameio.WriteFile(s.path, data, checkpointFilePerm)
}

// flushWithRetry calls flush and on failure logs the error, re-marks dirty,
// and reschedules the timer for a later retry.
func (s *CheckpointStore) flushWithRetry() {
	if err := s.flush(); err != nil {
		slog.Error("failed to write checkpoint file", "path", s.path, "error", err)
		s.retryAfterFailure()
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

// snapshot atomically marks the timer as stopped, checks the dirty flag, and
// if dirty returns a serializable copy of the entries map with dirty cleared.
// Returns (nil, false) when no flush is needed.
func (s *CheckpointStore) snapshot() (map[string]Checkpoint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	if !s.dirty {
		return nil, false
	}
	s.dirty = false
	out := make(map[string]Checkpoint, len(s.entries))
	for k, v := range s.entries {
		out[compositeKey(k)] = v
	}
	return out, true
}

// Shutdown stops the checkpoint store: cancels the watch goroutine, waits for
// any in-flight timer-scheduled flush, and runs a synchronous final flush.
func (s *CheckpointStore) Shutdown(ctx context.Context) error {
	s.stopTimer()
	s.cg.Cancel()
	if err := s.cg.WaitContext(ctx); err != nil {
		return err
	}
	return s.flush()
}

// ShutdownTimeout calls Shutdown with a timeout-bounded context.
func (s *CheckpointStore) ShutdownTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.Shutdown(ctx)
}

// stopTimer stops the debounce timer under the mutex. The timer is nil if the
// store was never mutated.
func (s *CheckpointStore) stopTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.stopped = true
}
