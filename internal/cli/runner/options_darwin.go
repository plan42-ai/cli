package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/plan42-ai/cli/internal/p42runtime"
	"github.com/plan42-ai/cli/internal/p42runtime/apple"
	"github.com/plan42-ai/cli/internal/p42runtime/podman"
	"github.com/plan42-ai/cli/internal/poller"
)

const runnerAgentLabel = "ai.plan42.runner"

type PlatformOptions struct {
	ContainerPath string              `help:"Path to the container executable" default:"/opt/homebrew/bin/container"`
	PodmanPath    string              `help:"Path to the podman executable" default:"podman"`
	Provider      p42runtime.Provider `kong:"-"`
	runtime       string
}

func (p *PlatformOptions) PollerOptions(options []poller.Option) []poller.Option {
	if p.Provider != nil {
		options = append(options, poller.WithProvider(p.Provider))
	}
	options = append(options, poller.WithContainerPath(p.ContainerPath))
	options = append(options, poller.WithPodmanPath(p.PodmanPath))
	return options
}

func (p *PlatformOptions) SetupRuntime(runtimeName string) error {
	logDir, err := runnerLogDir()
	if err != nil {
		return fmt.Errorf("failed to determine log directory: %w", err)
	}

	p.runtime = runtimeName
	switch runtimeName {
	case p42runtime.RuntimeApple:
		p.Provider = apple.NewProvider(p.ContainerPath, logDir)
	case p42runtime.RuntimePodman:
		p.Provider = podman.NewProvider(p.PodmanPath, logDir)
	default:
		return fmt.Errorf("unsupported runtime: %s", runtimeName)
	}

	return nil
}

func (p *PlatformOptions) Init(ctx context.Context) error {
	if p.Provider == nil {
		return fmt.Errorf("runtime provider not configured")
	}

	switch p.runtime {
	case p42runtime.RuntimePodman:
		if !p.Provider.IsInstalled() {
			return fmt.Errorf("podman is not installed on the local runner; update the [runner] runtime in the config or install podman")
		}
		return nil
	default:
		if !p.Provider.IsInstalled() {
			return fmt.Errorf("apple container runtime is not installed on the local runner; update the [runner] runtime or install the Apple runtime")
		}
		slog.InfoContext(ctx, "running `container system start`", "container_path", p.ContainerPath)
		// #nosec G204: ContainerPath is user-configurable and validated separately.
		cmd := exec.CommandContext(ctx, p.ContainerPath, "system", "start")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("container system start failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
}

func runnerLogDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, "Library", "Logs", runnerAgentLabel), nil
}
