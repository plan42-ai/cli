package poller

import (
	"github.com/plan42-ai/cli/internal/github"
	containerruntime "github.com/plan42-ai/cli/internal/runtime"
)

type PlatformFields struct {
	runtimeProvider containerruntime.Provider
}

type InvokePlatformFields struct {
	runtimeProvider containerruntime.Provider
	githubClient    *github.Client
}

func WithRuntimeProvider(provider containerruntime.Provider) Option {
	return func(p *Poller) {
		p.runtimeProvider = provider
	}
}
