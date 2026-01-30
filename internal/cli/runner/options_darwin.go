package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/plan42-ai/cli/internal/poller"
	"github.com/plan42-ai/cli/internal/runtime"
)

type PlatformOptions struct {
	ContainerPath string `help:"Path to the container executable" default:"/opt/homebrew/bin/container"`
	PodmanPath    string `help:"Path to the podman executable" default:"podman"`
	provider      runtime.RuntimeProvider
}

func (p *PlatformOptions) PollerOptions(options []poller.Option, runtimeConfig string) []poller.Option {
	p.initProvider(runtimeConfig)
	options = append(options, poller.WithContainerPath(p.ContainerPath))
	if p.provider != nil {
		options = append(options, poller.WithRuntimeProvider(p.provider))
	}
	return options
}

func (p *PlatformOptions) initProvider(runtimeConfig string) {
	switch runtimeConfig {
	case "podman":
		p.provider = runtime.NewPodmanProvider(p.PodmanPath)
	case "apple", "":
		// Default to Apple provider
		p.provider = runtime.NewAppleProvider(p.ContainerPath)
	default:
		// Unknown runtime, fall back to Apple
		p.provider = runtime.NewAppleProvider(p.ContainerPath)
	}
}

func (p *PlatformOptions) Init(ctx context.Context, runtimeConfig string) error {
	p.initProvider(runtimeConfig)

	// Only run system start for Apple containers
	if runtimeConfig == "apple" || runtimeConfig == "" {
		slog.InfoContext(ctx, "running `container system start`", "container_path", p.ContainerPath)
		// #nosec G204: ContainerPath is a local configuration value under user control with limited scope.
		cmd := exec.CommandContext(ctx, p.ContainerPath, "system", "start")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("container system start failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}
