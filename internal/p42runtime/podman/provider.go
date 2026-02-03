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

	"github.com/plan42-ai/cli/internal/p42runtime"
	"github.com/plan42-ai/cli/internal/util"
)

const jobPrefix = "plan42-"

type Provider struct {
	podmanPath string
	logDir     string
}

func NewProvider(podmanPath string, logDir string) *Provider {
	if podmanPath == "" {
		podmanPath = "podman"
	}
	return &Provider{
		podmanPath: podmanPath,
		logDir:     logDir,
	}
}

func (p *Provider) Name() string {
	return "podman"
}

func (p *Provider) IsInstalled() bool {
	_, err := exec.LookPath(p.podmanPath)
	return err == nil
}

func (p *Provider) PullImage(ctx context.Context, image string) error {
	// #nosec G204: Subprocess launched with a potential tainted input or cmd arguments
	//     podmanPath is user-configurable. image is validated before reaching this method.
	cmd := exec.CommandContext(ctx, p.podmanPath, "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w\n%s", image, err, string(output))
	}
	return nil
}

func (p *Provider) RunJob(ctx context.Context, opts p42runtime.JobOptions) error {
	args := []string{"run", "--rm"}

	if opts.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(opts.CPUs))
	}
	if opts.MemoryInGB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dG", opts.MemoryInGB))
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

	args = append(args, opts.Image)
	args = append(args, opts.Args...)

	// #nosec G204: Subprocess launched with a potential tainted input or cmd arguments
	//     podmanPath is user-configurable and opts are validated before invocation.
	cmd := exec.CommandContext(ctx, p.podmanPath, args...)
	cmd.Stdin = opts.Stdin

	if opts.JobID != "" && p.logDir != "" {
		logPath := filepath.Join(p.logDir, opts.JobID)
		if err := os.MkdirAll(p.logDir, 0o755); err != nil {
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

func (p *Provider) KillJob(ctx context.Context, jobID string) error {
	// #nosec G204: Subprocess launched with a potential tainted input or cmd arguments
	//     podmanPath is user-configurable and jobID is validated upstream.
	cmd := exec.CommandContext(ctx, p.podmanPath, "kill", jobID)
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

func (p *Provider) GetRunningJobIDs(ctx context.Context) ([]string, error) {
	// #nosec G204: Subprocess launched with a potential tainted input or cmd arguments
	//     podmanPath is user-configurable and is validated separately.
	output, err := exec.CommandContext(ctx, p.podmanPath, "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var ids []string
	reader := bufio.NewReader(bytes.NewReader(output))
	for {
		line, _, readErr := reader.ReadLine()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		name := strings.TrimSpace(string(line))
		if name == "" || !strings.HasPrefix(name, jobPrefix) {
			continue
		}
		ids = append(ids, name)
	}

	return ids, nil
}

func (p *Provider) GetAllJobIDs(ctx context.Context) ([]string, error) {
	_ = ctx
	if p.logDir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(p.logDir)
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
		if !strings.HasPrefix(name, jobPrefix) {
			continue
		}
		ids = append(ids, name)
	}

	return ids, nil
}

func (p *Provider) ValidateJobID(jobID string) error {
	if !strings.HasPrefix(jobID, jobPrefix) {
		return fmt.Errorf("invalid job id: %s", jobID)
	}

	trimmed := strings.TrimPrefix(jobID, jobPrefix)
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

func (p *Provider) DeleteJobLog(jobID string) error {
	if err := p.ValidateJobID(jobID); err != nil {
		return err
	}

	if p.logDir == "" {
		return nil
	}

	logPath := filepath.Join(p.logDir, jobID)

	err := os.Remove(logPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}
