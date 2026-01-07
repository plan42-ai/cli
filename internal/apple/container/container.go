package container

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
)

const (
	RunnerAgentLabel = "ai.plan42.runner"
	containerPrefix  = "plan42-"
	maxConcurrency   = 10
)

type Job struct {
	CreatedDate time.Time
	TaskID      string
	TaskTitle   string
	TurnIndex   int
	Running     bool
}

func GetLocalJobs(ctx context.Context, client *p42.Client, tenantID string, verbose bool, all bool) ([]*Job, error) {
	jobCh := make(chan *Job, maxConcurrency)
	var wg sync.WaitGroup

	startWorkers(ctx, client, tenantID, verbose, jobCh, &wg)

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

	runningJobs, err := gatherRunningJobs(ctx, jobs, jobCh, running)
	if err != nil {
		return nil, err
	}
	jobs = runningJobs

	if all {
		completedJobs, err := gatherCompletedJobs(jobs, running, jobCh)
		if err != nil {
			return nil, err
		}
		jobs = completedJobs
	}

	cleanup()
	sortJobs(jobs)

	return jobs, nil
}

func startWorkers(ctx context.Context, client *p42.Client, tenantID string, verbose bool, jobCh <-chan *Job, wg *sync.WaitGroup) {
	for i := 0; i < maxConcurrency; i++ {
		wg.Add(1)
		go worker(ctx, client, tenantID, verbose, jobCh, wg)
	}
}

func worker(ctx context.Context, client *p42.Client, tenantID string, verbose bool, jobCh <-chan *Job, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range jobCh {
		task, err := client.GetTask(ctx, &p42.GetTaskRequest{
			TenantID:       tenantID,
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

		turn, err := client.GetTurn(
			ctx,
			&p42.GetTurnRequest{
				TenantID:       tenantID,
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

func gatherRunningJobs(ctx context.Context, jobs []*Job, jobCh chan<- *Job, running map[string]bool) ([]*Job, error) {
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

func gatherCompletedJobs(jobs []*Job, running map[string]bool, jobCh chan<- *Job) ([]*Job, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	logDir := filepath.Join(homeDir, "Library", "Logs", RunnerAgentLabel)
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

func ValidateJobID(jobID string) error {
	if _, ok := buildJob(jobID, true); !ok {
		return fmt.Errorf("invalid job id: %s", jobID)
	}

	return nil
}

func KillJob(jobID string) error {
	cmd := exec.Command("container", "kill", jobID)
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
