package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pelletier/go-toml/v2"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/concurrency"
)

type Loader struct {
	cg         *concurrency.ContextGroup
	watcher    *fsnotify.Watcher
	configPath string
	watchDir   string
	hasWatch   bool
	cfg        atomic.Pointer[Config]
}

func NewLoader(configPath string) (loader *Loader, err error) {
	defer func() {
		if err != nil && loader != nil {
			util.Close(loader)
		}
	}()

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve config path: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("unable to create watcher for config directory: %w", err)
	}

	loader = &Loader{
		cg:         concurrency.NewContextGroup(),
		watcher:    watcher,
		configPath: absPath,
		watchDir:   filepath.Dir(absPath),
	}

	err = loader.load()
	if err != nil {
		return nil, err
	}

	err = loader.ensureWatches()
	if err != nil {
		return nil, err
	}

	loader.cg.Add(1)
	go loader.watch()

	return loader, nil
}

func (l *Loader) Current() *Config {
	return l.cfg.Load()
}

func (l *Loader) Close() error {
	var err1, err2 error
	if l.watcher != nil {
		err1 = l.watcher.Close()
	}

	if l.cg != nil {
		err2 = l.cg.Close()
	}

	return util.Coalesce(err1, err2)
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
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-l.cg.Context().Done():
			return
		case <-ticker.C:
			err := l.ensureWatches()
			if err != nil {
				slog.ErrorContext(l.cg.Context(), "failed to update config watcher", "error", err)
			}
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
	info, err := os.Stat(l.watchDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if l.hasWatch {
				_ = l.watcher.Remove(l.watchDir)
				l.hasWatch = false
			}
			return nil
		}
		return fmt.Errorf("error accessing path '%s': %w", l.watchDir, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("watch path %s is not a directory", l.watchDir)
	}

	if l.hasWatch {
		return nil
	}

	err = l.watcher.Add(l.watchDir)
	if err != nil {
		return fmt.Errorf("unable to create watcher for config directory: %w", err)
	}

	l.hasWatch = true
	return nil
}

func (l *Loader) isRelevant(path string) bool {
	cleaned := filepath.Clean(path)
	if cleaned == l.configPath {
		return true
	}

	return cleaned == filepath.Dir(l.configPath)
}
