// Package apple implements the RuntimeProvider interface for Apple's container runtime.
package apple

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

	"github.com/plan42-ai/cli/internal/runtime"
	"github.com/plan42-ai/cli/internal/util"
)

const (
	containerPrefix  = "plan42-"
	runnerAgentLabel = "ai.plan42.runner"
)

// Provider implements RuntimeProvider for Apple's container runtime.
type Provider struct {
	containerPath string
}

// NewProvider creates a new Provider with the given container binary path.
// If containerPath is empty, it defaults to "container".
func NewProvider(containerPath string) *Provider {
	if containerPath == "" {
		containerPath = "container"
	}
	return &Provider{
		containerPath: containerPath,
	}
}

// Name returns the configuration name of the runtime.
func (p *Provider) Name() string {
	return "apple"
}

// IsInstalled reports whether the container binary is available on the system.
func (p *Provider) IsInstalled() bool {
	_, err := exec.LookPath(p.containerPath)
	return err == nil
}

// PullImage pulls the specified container image.
func (p *Provider) PullImage(ctx context.Context, image string) error {
	// #nosec G204: Subprocess launched with a potential tainted input or cmd arguments
	//     containerPath is user-configurable, but we intentionally allow users to specify
	//     their container binary location. image is validated before reaching this method.
	cmd := exec.CommandContext(ctx, p.containerPath, "image", "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w\n%s", image, err, string(output))
	}
	return nil
}

// RunJob runs a job with the specified options.
// The provider computes the log file path internally based on JobID,
// storing logs in ~/Library/Logs/ai.plan42.runner/{JobID}.
func (p *Provider) RunJob(ctx context.Context, opts runtime.JobOptions) error {
	args := []string{"run"}

	if opts.CPUs > 0 {
		args = append(args, "-c", strconv.Itoa(opts.CPUs))
	}
	if opts.Memory > 0 {
		args = append(args, "-m", fmt.Sprintf("%dG", opts.Memory))
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

	// #nosec G204: Subprocess launched with a potential tainted input or cmd arguments
	//     containerPath is user-configurable, but we intentionally allow users to specify
	//     their container binary location. JobID and Image are validated before reaching
	//     this method.
	cmd := exec.CommandContext(ctx, p.containerPath, args...)
	cmd.Stdin = opts.Stdin

	// Compute log path internally based on JobID
	if opts.JobID != "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}

		logPath := filepath.Join(homeDir, "Library", "Logs", runnerAgentLabel, opts.JobID)
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
		logFile, err := os.Create(logPath)
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

// KillJob terminates the job with the given ID.
// This streams output directly to os.Stdout/os.Stderr and panics on exit error,
// matching the original behavior in container.go.
func (p *Provider) KillJob(ctx context.Context, jobID string) error {
	// #nosec G204: Subprocess launched with a potential tainted input or cmd arguments
	//     containerPath is user-configurable, but we intentionally allow users to specify
	//     their container binary location. jobID is validated before reaching this method.
	cmd := exec.CommandContext(ctx, p.containerPath, "kill", jobID)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			panic(util.ExitCode(exitErr.ExitCode()))
		}
		return err
	}

	return nil
}

// GetRunningJobIDs returns IDs of all running containers matching the plan42-* pattern.
func (p *Provider) GetRunningJobIDs(ctx context.Context) ([]string, error) {
	// #nosec G204: Subprocess launched with a potential tainted input or cmd arguments
	//     containerPath is user-configurable, but we intentionally allow users to specify
	//     their container binary location.
	output, err := exec.CommandContext(ctx, p.containerPath, "ls").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var ids []string
	reader := bufio.NewReader(bytes.NewReader(output))
	lineIndex := 0
	for {
		line, _, readErr := reader.ReadLine()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		lineIndex++
		if lineIndex == 1 {
			continue // skip header
		}

		fields := strings.Fields(string(line))
		if len(fields) == 0 {
			continue
		}

		containerID := fields[0]
		if !strings.HasPrefix(containerID, containerPrefix) {
			continue
		}
		ids = append(ids, containerID)
	}

	return ids, nil
}

// GetCompletedJobIDs returns IDs of all completed jobs with log files.
// Log files are stored in ~/Library/Logs/ai.plan42.runner/.
func (p *Provider) GetCompletedJobIDs() ([]string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	logDir := filepath.Join(homeDir, "Library", "Logs", runnerAgentLabel)
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read log directory: %w", err)
	}

	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, containerPrefix) {
			continue
		}
		ids = append(ids, name)
	}

	return ids, nil
}

// ValidateJobID checks if the given job ID is valid for this runtime.
// A valid job ID has the format "plan42-{taskID}-{turnIndex}".
func (p *Provider) ValidateJobID(jobID string) error {
	if !strings.HasPrefix(jobID, containerPrefix) {
		return fmt.Errorf("invalid job id: %s", jobID)
	}

	trimmed := strings.TrimPrefix(jobID, containerPrefix)
	idx := strings.LastIndex(trimmed, "-")
	if idx == -1 {
		return fmt.Errorf("invalid job id: %s", jobID)
	}

	_, err := strconv.Atoi(trimmed[idx+1:])
	if err != nil {
		return fmt.Errorf("invalid job id: %s", jobID)
	}

	return nil
}

// DeleteJobLog removes the log file for the specified job.
// Log files are stored in ~/Library/Logs/ai.plan42.runner/.
func (p *Provider) DeleteJobLog(jobID string) error {
	if err := p.ValidateJobID(jobID); err != nil {
		return err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	logPath := filepath.Join(homeDir, "Library", "Logs", runnerAgentLabel, jobID)

	err = os.Remove(logPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}
