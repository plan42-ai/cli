package poller

import "github.com/plan42-ai/cli/internal/github"

type PlatformFields struct {
	ContainerPath string
}

type InvokePlatformFields struct {
	ContainerPath string
	githubClient  *github.Client
}

func WithContainerPath(path string) Option {
	return func(p *Poller) {
		p.ContainerPath = path
	}
}
