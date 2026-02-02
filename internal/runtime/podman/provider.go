// Package podman implements the runtime.Provider interface for Podman.
package podman

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	containerruntime "github.com/plan42-ai/cli/internal/runtime"
)

const (
	containerPrefix  = containerruntime.JobNamePrefix
	runnerAgentLabel = containerruntime.RunnerAgentLabel
)

// Provider implements runtime.Provider for Podman.
type Provider struct {
	podmanPath string
}

// NewProvider creates a new Provider with the given podman binary path.
// If podmanPath is empty, it defaults to "podman".
func NewProvider(podmanPath string) *Provider {
	if podmanPath == "" {
		podmanPath = "podman"
	}
	return &Provider{
		podmanPath: podmanPath,
	}
}

// Name returns the human-readable name of the runtime.
func (p *Provider) Name() string {
	return containerruntime.RuntimePodman
}

// IsInstalled reports whether the podman binary is available on the system.
func (p *Provider) IsInstalled() bool {
	_, err := exec.LookPath(p.podmanPath)
	return err == nil
}

// Validate checks that the runtime is properly configured and functional.
func (p *Provider) Validate(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.podmanPath, "--version") //nolint:gosec // podmanPath is controlled
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to validate Podman runtime: %w", err)
	}
	return nil
}

// PullImage pulls the specified container image.
func (p *Provider) PullImage(ctx context.Context, image string) error {
	cmd := exec.CommandContext(ctx, p.podmanPath, "pull", image) //nolint:gosec // podmanPath is controlled
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w\n%s", image, err, string(output))
	}
	return nil
}

// RunJob runs a job with the specified options.
func (p *Provider) RunJob(ctx context.Context, opts containerruntime.JobOptions) error {
	args := []string{"run"}

	if opts.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(opts.CPUs))
	}
	if opts.Memory > 0 {
		args = append(args, "--memory", formatMemory(opts.Memory))
	}
	if opts.JobID != "" {
		args = append(args, "--name", opts.JobID)
	}
	if opts.Stdin != nil {
		args = append(args, "-i")
	}
	if opts.Entrypoint != "" {
		args = append(args, "--entrypoint", opts.Entrypoint)
	}

	args = append(args, "--rm")
	args = append(args, opts.Image)
	args = append(args, opts.Args...)

	cmd := exec.CommandContext(ctx, p.podmanPath, args...) //nolint:gosec // podmanPath is controlled
	cmd.Stdin = opts.Stdin

	if opts.LogPath != "" {
		logFile, err := os.Create(opts.LogPath)
		if err != nil {
			return fmt.Errorf("failed to create log file: %w", err)
		}
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		cmd.Stdout = opts.Stdout
		cmd.Stderr = opts.Stderr
	}

	return cmd.Run()
}

// ListJobs returns all jobs managed by this runtime.
func (p *Provider) ListJobs(ctx context.Context) ([]*containerruntime.Job, error) {
	jobs := make([]*containerruntime.Job, 0)
	running := make(map[string]bool)

	output, err := exec.CommandContext(ctx, p.podmanPath, "ps", "--format", "{{.Names}}", "--filter", "name=^plan42-").Output() //nolint:gosec // podmanPath is controlled
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	reader := bufio.NewReader(bytes.NewReader(output))
	for {
		line, _, readErr := reader.ReadLine()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		containerID := strings.TrimSpace(string(line))
		if containerID == "" {
			continue
		}
		job, ok := buildJob(containerID, true)
		if !ok {
			continue
		}
		running[containerID] = true
		jobs = append(jobs, job)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	logDir := filepath.Join(homeDir, "Library", "Logs", runnerAgentLabel)
	entries, dirErr := os.ReadDir(logDir)
	if dirErr != nil {
		if errors.Is(dirErr, os.ErrNotExist) {
			return jobs, nil
		}
		return nil, fmt.Errorf("failed to read log directory: %w", dirErr)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if running[name] {
			continue
		}
		job, ok := buildJob(name, false)
		if !ok {
			continue
		}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

// KillJob terminates the job with the given ID.
func (p *Provider) KillJob(ctx context.Context, jobID string) error {
	cmd := exec.CommandContext(ctx, p.podmanPath, "kill", jobID) //nolint:gosec // podmanPath is controlled
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to kill job %s: %w\n%s", jobID, err, string(output))
	}
	return nil
}

// buildJob parses a container ID into a Job struct.
func buildJob(containerID string, running bool) (*containerruntime.Job, bool) {
	if !strings.HasPrefix(containerID, containerPrefix) {
		return nil, false
	}

	trimmed := strings.TrimPrefix(containerID, containerPrefix)
	idx := strings.LastIndex(trimmed, "-")
	if idx == -1 {
		return nil, false
	}

	turnIndex, err := strconv.Atoi(trimmed[idx+1:])
	if err != nil {
		return nil, false
	}

	return &containerruntime.Job{
		TaskID:    trimmed[:idx],
		TurnIndex: turnIndex,
		Running:   running,
	}, true
}

// formatMemory converts bytes to a human-readable format for the container command.
func formatMemory(bytes int64) string {
	const (
		gb = 1024 * 1024 * 1024
		mb = 1024 * 1024
	)
	if bytes >= gb && bytes%gb == 0 {
		return fmt.Sprintf("%dG", bytes/gb)
	}
	if bytes >= mb && bytes%mb == 0 {
		return fmt.Sprintf("%dM", bytes/mb)
	}
	return strconv.FormatInt(bytes, 10)
}
