package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"github.com/pelletier/go-toml/v2"
	"github.com/plan42-ai/concurrency"
)

type Loader struct {
	cg         *concurrency.ContextGroup
	watcher    *fsnotify.Watcher
	configPath string
	cfg        atomic.Pointer[Config]
	watchMu    sync.Mutex
	watched    map[string]struct{}
}

func NewLoader(ctx context.Context, configPath string) (*Loader, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	l := &Loader{
		cg:         concurrency.NewContextGroup(),
		watcher:    watcher,
		configPath: absPath,
		watched:    make(map[string]struct{}),
	}

	l.cfg.Store(&Config{})

	err = l.load()
	if err != nil {
		_ = watcher.Close()
		return nil, err
	}

	err = l.ensureWatches()
	if err != nil {
		_ = watcher.Close()
		return nil, err
	}

	l.cg.Add(1)
	go l.watch()

	l.cg.Add(1)
	go func() {
		defer l.cg.Done()
		select {
		case <-ctx.Done():
			l.cg.Cancel()
		case <-l.cg.Context().Done():
		}
	}()

	return l, nil
}

func (l *Loader) Current() *Config {
	return l.cfg.Load()
}

func (l *Loader) Close() error {
	return l.cg.Close()
}

func (l *Loader) load() error {
	file, err := os.Open(l.configPath)
	if err != nil {
		return fmt.Errorf("open config file: %w", err)
	}
	defer file.Close()

	decoder := toml.NewDecoder(file)
	var cfg Config
	err = decoder.Decode(&cfg)
	if err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}

	l.cfg.Store(&cfg)
	return nil
}

func (l *Loader) watch() {
	defer l.cg.Cancel()
	defer l.cg.Done()
	defer l.watcher.Close()

	for {
		select {
		case <-l.cg.Context().Done():
			return
		case err, ok := <-l.watcher.Errors:
			if !ok {
				return
			}
			slog.ErrorContext(l.cg.Context(), "config watcher error", "error", err)
		case event, ok := <-l.watcher.Events:
			if !ok {
				return
			}

			err := l.ensureWatches()
			if err != nil {
				slog.ErrorContext(l.cg.Context(), "failed to update config watcher", "error", err)
				continue
			}

			if !l.isRelevant(event.Name) {
				continue
			}

			err = l.load()
			if err != nil {
				slog.ErrorContext(l.cg.Context(), "failed to reload config", "error", err)
			}
		}
	}
}

func (l *Loader) ensureWatches() error {
	l.watchMu.Lock()
	defer l.watchMu.Unlock()

	desired := make(map[string]struct{})
	dir := filepath.Dir(l.configPath)
	parent := filepath.Dir(dir)
	paths := []string{parent}
	if dir != parent {
		paths = append(paths, dir)
	}

	for _, p := range paths {
		desired[p] = struct{}{}
		info, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if _, ok := l.watched[p]; ok {
					_ = l.watcher.Remove(p)
					delete(l.watched, p)
				}
				continue
			}
			return fmt.Errorf("stat watch path %s: %w", p, err)
		}

		if !info.IsDir() {
			return fmt.Errorf("watch path %s is not a directory", p)
		}

		if _, ok := l.watched[p]; ok {
			continue
		}

		err = l.watcher.Add(p)
		if err != nil {
			return fmt.Errorf("add watch for %s: %w", p, err)
		}
		l.watched[p] = struct{}{}
	}

	for p := range l.watched {
		if _, ok := desired[p]; !ok {
			_ = l.watcher.Remove(p)
			delete(l.watched, p)
		}
	}

	return nil
}

func (l *Loader) isRelevant(path string) bool {
	cleaned := filepath.Clean(path)
	if cleaned == l.configPath {
		return true
	}

	return cleaned == filepath.Dir(l.configPath)
}
