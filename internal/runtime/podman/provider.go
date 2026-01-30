// Package podman implements the RuntimeProvider interface for Podman containers.
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
	goruntime "runtime"
	"strconv"
	"strings"

	rt "github.com/plan42-ai/cli/internal/runtime"
)

const (
	containerPrefix = "plan42-"
	logLabel        = "ai.plan42.runner"
)

// Provider implements runtime.RuntimeProvider for Podman containers.
type Provider struct {
	podmanPath string
}

// NewProvider creates a new Podman runtime provider.
// If podmanPath is empty, it defaults to "podman".
func NewProvider(podmanPath string) *Provider {
	if podmanPath == "" {
		podmanPath = "podman"
	}
	return &Provider{podmanPath: podmanPath}
}

// Name returns the human-readable name of the runtime.
func (p *Provider) Name() string {
	return "Podman"
}

// IsInstalled reports whether Podman is available on the system.
func (p *Provider) IsInstalled() bool {
	_, err := exec.LookPath(p.podmanPath)
	return err == nil
}

// Validate checks that Podman is properly configured and functional.
func (p *Provider) Validate(ctx context.Context) error {
	// Check that podman command works
	cmd := exec.CommandContext(ctx, p.podmanPath, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman --version failed: %w", err)
	}

	// On macOS, Podman runs containers in a Linux VM.
	// Verify that the Podman machine is running.
	if goruntime.GOOS == "darwin" {
		if err := p.validateMachineRunning(ctx); err != nil {
			return err
		}
	}

	return nil
}

// validateMachineRunning checks that the Podman machine is running on macOS.
func (p *Provider) validateMachineRunning(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.podmanPath, "machine", "info", "--format", "{{.Host.MachineState}}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("podman machine info failed: %w", err)
	}

	state := strings.TrimSpace(string(output))
	if state != "Running" {
		return fmt.Errorf("podman machine is not running (state: %s), run 'podman machine start'", state)
	}

	return nil
}

// PullImage pulls the specified container image.
func (p *Provider) PullImage(ctx context.Context, image string) error {
	cmd := exec.CommandContext(ctx, p.podmanPath, "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman pull failed: %w\noutput: %s", err, string(output))
	}
	return nil
}

// RunContainer runs a container with the specified options.
func (p *Provider) RunContainer(ctx context.Context, opts rt.ContainerOptions) error {
	args := []string{
		"run",
		"--cpus", strconv.Itoa(opts.CPUs),
		"--memory", formatMemory(opts.Memory),
		"--name", opts.ContainerID,
		"-i",
		"--rm",
	}

	if opts.Entrypoint != "" {
		args = append(args, "--entrypoint", opts.Entrypoint)
	}

	args = append(args, opts.Image)
	args = append(args, opts.Args...)

	cmd := exec.CommandContext(ctx, p.podmanPath, args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr

	return cmd.Run()
}

// formatMemory converts bytes to a format Podman accepts.
// We use raw bytes to avoid rounding issues that could cause OOMs.
func formatMemory(bytes int64) string {
	return fmt.Sprintf("%d", bytes)
}

// ListJobs returns all jobs managed by this runtime.
func (p *Provider) ListJobs(ctx context.Context) ([]*rt.Job, error) {
	jobs := make([]*rt.Job, 0)
	running := make(map[string]bool)

	// Get running containers
	runningJobs, err := p.listRunningContainers(ctx, running)
	if err != nil {
		return nil, err
	}
	jobs = append(jobs, runningJobs...)

	// Get completed jobs from logs directory
	completedJobs, err := p.listCompletedJobs(running)
	if err != nil {
		return nil, err
	}
	jobs = append(jobs, completedJobs...)

	return jobs, nil
}

// listRunningContainers returns running Plan42 containers.
func (p *Provider) listRunningContainers(ctx context.Context, running map[string]bool) ([]*rt.Job, error) {
	cmd := exec.CommandContext(ctx, p.podmanPath, "ps", "--format", "{{.Names}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("podman ps failed: %w", err)
	}

	jobs := make([]*rt.Job, 0)
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

	return jobs, nil
}

// listCompletedJobs returns completed jobs from the logs directory.
func (p *Provider) listCompletedJobs(running map[string]bool) ([]*rt.Job, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	logDir := filepath.Join(homeDir, "Library", "Logs", logLabel)
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	jobs := make([]*rt.Job, 0)
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

// buildJob parses a container ID into a Job struct.
func buildJob(containerID string, isRunning bool) (*rt.Job, bool) {
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

	return &rt.Job{
		TaskID:    trimmed[:idx],
		TurnIndex: turnIndex,
		Running:   isRunning,
	}, true
}

// KillJob terminates the job with the given ID.
func (p *Provider) KillJob(ctx context.Context, jobID string) error {
	cmd := exec.CommandContext(ctx, p.podmanPath, "kill", jobID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman kill failed: %w\noutput: %s", err, string(output))
	}
	return nil
}
