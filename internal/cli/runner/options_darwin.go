package runner

import "github.com/plan42-ai/cli/internal/poller"

type PlatformOptions struct {
	ContainerPath string `help:"Path to the container executable" default:"/opt/homebrew/bin/container"`
}

func (p *PlatformOptions) PollerOptions(options []poller.Option) []poller.Option {
	return append(
		options,
		poller.WithContainerPath(p.ContainerPath),
	)
}
