package poller

import (
	"github.com/plan42-ai/cli/internal/github"
	"github.com/plan42-ai/cli/internal/runtime"
)

type PlatformFields struct {
	ContainerPath string
}

type InvokePlatformFields struct {
	ContainerPath string
	githubClient  *github.Client
	provider      runtime.RuntimeProvider
}

func WithContainerPath(path string) Option {
	return func(p *Poller) {
		p.ContainerPath = path
	}
}
