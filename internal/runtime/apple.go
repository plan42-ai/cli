package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
)

const (
	runnerAgentLabel = "ai.plan42.runner"
	containerPrefix  = "plan42-"
	maxConcurrency   = 10
)

// AppleProvider implements RuntimeProvider for Apple's container runtime.
type AppleProvider struct {
	client   *p42.Client
	tenantID string
}

// NewAppleProvider creates a new Apple container runtime provider.
func NewAppleProvider(client *p42.Client, tenantID string) *AppleProvider {
	return &AppleProvider{
		client:   client,
		tenantID: tenantID,
	}
}

func (p *AppleProvider) Name() string {
	return "Apple"
}

func (p *AppleProvider) IsInstalled() bool {
	_, err := exec.LookPath("container")
	return err == nil
}

func (p *AppleProvider) Validate(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "container", "--version")
	return cmd.Run()
}

func (p *AppleProvider) PullImage(_ context.Context, _ string) error {
	return errors.New("not implemented")
}

func (p *AppleProvider) RunContainer(_ context.Context, _ ContainerOptions) error {
	return errors.New("not implemented")
}

func (p *AppleProvider) ListJobs(ctx context.Context, opts ListJobsOptions) ([]*Job, error) {
	jobCh := make(chan *Job, maxConcurrency)
	var wg sync.WaitGroup

	p.startWorkers(ctx, opts.Verbose, jobCh, &wg)

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			close(jobCh)
			wg.Wait()
		})
	}
	defer cleanup()

	jobs := make([]*Job, 0)
	running := make(map[string]bool)

	runningJobs, err := p.gatherRunningJobs(ctx, jobs, jobCh, running)
	if err != nil {
		return nil, err
	}
	jobs = runningJobs

	if opts.All {
		completedJobs, err := p.gatherCompletedJobs(jobs, running, jobCh)
		if err != nil {
			return nil, err
		}
		jobs = completedJobs
	}

	cleanup()
	sortJobs(jobs)

	return jobs, nil
}

func (p *AppleProvider) KillJob(ctx context.Context, jobID string) error {
	cmd := exec.CommandContext(ctx, "container", "kill", jobID)
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

func (p *AppleProvider) startWorkers(ctx context.Context, verbose bool, jobCh <-chan *Job, wg *sync.WaitGroup) {
	for i := 0; i < maxConcurrency; i++ {
		wg.Add(1)
		go p.worker(ctx, verbose, jobCh, wg)
	}
}

func (p *AppleProvider) worker(ctx context.Context, verbose bool, jobCh <-chan *Job, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range jobCh {
		task, err := p.client.GetTask(ctx, &p42.GetTaskRequest{
			TenantID:       p.tenantID,
			TaskID:         job.TaskID,
			IncludeDeleted: util.Pointer(true),
		})
		if err != nil {
			if verbose {
				slog.ErrorContext(ctx, "GetTask failed", "taskID", job.TaskID, "error", err)
			}
		} else {
			job.TaskTitle = task.Title
		}

		turn, err := p.client.GetTurn(
			ctx,
			&p42.GetTurnRequest{
				TenantID:       p.tenantID,
				TaskID:         job.TaskID,
				TurnIndex:      job.TurnIndex,
				IncludeDeleted: util.Pointer(true),
			},
		)
		if err != nil {
			if verbose {
				slog.ErrorContext(
					ctx,
					"GetTurn failed",
					slog.String("taskID", job.TaskID),
					slog.Int("turnIndex", job.TurnIndex),
					slog.Any("error", err),
				)
			}
			continue
		}
		job.CreatedDate = turn.CreatedAt
	}
}

func (p *AppleProvider) gatherRunningJobs(ctx context.Context, jobs []*Job, jobCh chan<- *Job, running map[string]bool) ([]*Job, error) {
	output, err := exec.CommandContext(ctx, "container", "ls").Output()
	if err != nil {
		return nil, err
	}

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
			continue
		}

		fields := strings.Fields(string(line))
		if len(fields) == 0 {
			continue
		}

		containerID := fields[0]
		job, ok := buildJob(containerID, true)
		if !ok {
			continue
		}
		running[containerID] = true
		jobs = append(jobs, job)
		jobCh <- job
	}

	return jobs, nil
}

func (p *AppleProvider) gatherCompletedJobs(jobs []*Job, running map[string]bool, jobCh chan<- *Job) ([]*Job, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	logDir := filepath.Join(homeDir, "Library", "Logs", runnerAgentLabel)
	entries, dirErr := os.ReadDir(logDir)
	if dirErr != nil {
		if errors.Is(dirErr, os.ErrNotExist) {
			return jobs, nil
		}
		return jobs, dirErr
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
		running[name] = true
		jobs = append(jobs, job)
		jobCh <- job
	}

	return jobs, nil
}

func sortJobs(jobs []*Job) {
	sort.Slice(jobs, func(i, j int) bool {
		left := jobs[i]
		right := jobs[j]
		if left.CreatedDate.Equal(right.CreatedDate) {
			if left.TaskTitle == right.TaskTitle {
				return left.TaskID < right.TaskID
			}
			return left.TaskTitle < right.TaskTitle
		}
		return left.CreatedDate.Before(right.CreatedDate)
	})
}

func buildJob(containerID string, running bool) (*Job, bool) {
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

	return &Job{
		TaskID:    trimmed[:idx],
		TurnIndex: turnIndex,
		Running:   running,
	}, true
}
