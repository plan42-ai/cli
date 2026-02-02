package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/plan42-ai/cli/internal/poller"
	containerruntime "github.com/plan42-ai/cli/internal/runtime"
	"github.com/plan42-ai/cli/internal/runtime/apple"
	"github.com/plan42-ai/cli/internal/runtime/podman"
)

type PlatformOptions struct {
	ContainerPath   string                    `help:"Path to the container executable" default:"/opt/homebrew/bin/container"`
	PodmanPath      string                    `help:"Path to the podman executable" default:"podman"`
	runtimeName     string                    `kong:"-"`
	runtimeProvider containerruntime.Provider `kong:"-"`
}

func (p *PlatformOptions) ConfigureRuntime(runtimeName string) error {
	switch runtimeName {
	case "", containerruntime.RuntimeApple:
		p.runtimeName = containerruntime.RuntimeApple
		p.runtimeProvider = apple.NewProvider(p.ContainerPath)
	case containerruntime.RuntimePodman:
		p.runtimeName = containerruntime.RuntimePodman
		p.runtimeProvider = podman.NewProvider(p.PodmanPath)
	default:
		return fmt.Errorf("unsupported runtime: %s", runtimeName)
	}

	return nil
}

func (p *PlatformOptions) PollerOptions(options []poller.Option) []poller.Option {
	if p.runtimeProvider != nil {
		options = append(options, poller.WithRuntimeProvider(p.runtimeProvider))
	}
	return options
}

func (p *PlatformOptions) Init(ctx context.Context) error {
	if p.runtimeProvider == nil {
		return fmt.Errorf("runtime provider not configured")
	}

	if !p.runtimeProvider.IsInstalled() {
		switch p.runtimeName {
		case containerruntime.RuntimePodman:
			return fmt.Errorf("podman is not installed on the local runner")
		default:
			return fmt.Errorf("apple container runtime is not installed on the local runner")
		}
	}

	if p.runtimeName == containerruntime.RuntimeApple {
		slog.InfoContext(ctx, "running `container system start`", "container_path", p.ContainerPath)
		cmd := exec.CommandContext(ctx, p.ContainerPath, "system", "start") //nolint:gosec // container path is controlled
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("container system start failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}

	if err := p.runtimeProvider.Validate(ctx); err != nil {
		return err
	}

	return nil
}
