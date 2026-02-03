package poller

import (
	"os"
	"path/filepath"

	"github.com/plan42-ai/cli/internal/github"
	"github.com/plan42-ai/cli/internal/p42runtime"
	"github.com/plan42-ai/cli/internal/p42runtime/apple"
)

const (
	// runnerAgentLabel is the launchctl agent label for the Plan42 runner service on macOS.
	runnerAgentLabel = "ai.plan42.runner"
)

type PlatformFields struct {
	ContainerPath string
	PodmanPath    string
	Provider      p42runtime.Provider
}

type InvokePlatformFields struct {
	ContainerPath string
	PodmanPath    string
	Provider      p42runtime.Provider
	githubClient  *github.Client
}

func WithContainerPath(path string) Option {
	return func(p *Poller) {
		p.ContainerPath = path
		if p.Provider != nil {
			return
		}
		logDir := ""
		if homeDir, err := os.UserHomeDir(); err == nil {
			logDir = filepath.Join(homeDir, "Library", "Logs", runnerAgentLabel)
		}
		p.Provider = apple.NewProvider(path, logDir)
	}
}

func WithProvider(provider p42runtime.Provider) Option {
	return func(p *Poller) {
		p.Provider = provider
	}
}

func WithPodmanPath(path string) Option {
	return func(p *Poller) {
		p.PodmanPath = path
	}
}
