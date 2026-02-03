package p42runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
)

const (
	// jobPrefix is the prefix for all Plan42 job IDs.
	jobPrefix = "plan42-"

	// maxConcurrency is the maximum number of concurrent API calls for fetching job data.
	maxConcurrency = 10
)

// parseJobID parses a job ID into its components.
// Format: "plan42-{taskID}-{turnIndex}"
// Returns error if format is invalid.
func parseJobID(id string) (taskID string, turnIndex int, err error) {
	if !strings.HasPrefix(id, jobPrefix) {
		return "", 0, fmt.Errorf("invalid job id: missing %q prefix", jobPrefix)
	}

	trimmed := strings.TrimPrefix(id, jobPrefix)
	idx := strings.LastIndex(trimmed, "-")
	if idx == -1 {
		return "", 0, fmt.Errorf("invalid job id: missing turn index separator")
	}

	turnIndex, err = strconv.Atoi(trimmed[idx+1:])
	if err != nil {
		return "", 0, fmt.Errorf("invalid job id: turn index is not a number")
	}

	taskID = trimmed[:idx]
	return taskID, turnIndex, nil
}

// fetchJobs populates TaskTitle and CreatedDate for each job by calling the P42 API.
// Jobs must have TaskID, TurnIndex, and Running already set.
// Uses worker goroutines for concurrent API calls.
func fetchJobs(ctx context.Context, jobs []*Job, client *p42.Client, tenantID string, verbose bool) {
	if len(jobs) == 0 {
		return
	}

	jobCh := make(chan *Job, maxConcurrency)
	var wg sync.WaitGroup

	// Start worker goroutines
	for i := 0; i < maxConcurrency; i++ {
		wg.Add(1)
		go fetchWorker(ctx, client, tenantID, verbose, jobCh, &wg)
	}

	// Send jobs to workers
	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)

	// Wait for all workers to complete
	wg.Wait()
}

// fetchWorker processes jobs from the channel and populates TaskTitle and CreatedDate.
func fetchWorker(ctx context.Context, client *p42.Client, tenantID string, verbose bool, jobCh <-chan *Job, wg *sync.WaitGroup) {
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
		} else {
			job.CreatedDate = turn.CreatedAt
		}
	}
}

// sortJobs sorts jobs by CreatedDate (descending - newest first), then TaskTitle, then TaskID.
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
		return left.CreatedDate.After(right.CreatedDate)
	})
}

// GetCompletedJobIDs returns IDs of jobs that have log files but are no longer running.
// It computes this as: all job IDs with logs - running job IDs.
func GetCompletedJobIDs(ctx context.Context, provider Provider) ([]string, error) {
	// Get all job IDs (from log files)
	allIDs, err := provider.GetAllJobIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all job IDs: %w", err)
	}

	// Get running job IDs
	runningIDs, err := provider.GetRunningJobIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get running job IDs: %w", err)
	}

	// Build set of running IDs
	running := make(map[string]bool)
	for _, id := range runningIDs {
		running[id] = true
	}

	// Filter out running jobs
	var completedIDs []string
	for _, id := range allIDs {
		if !running[id] {
			completedIDs = append(completedIDs, id)
		}
	}

	return completedIDs, nil
}

// GetJobs returns a fully populated, sorted list of jobs.
// It performs the following steps:
// 1. Gets running job IDs from provider.
// 2. Optionally gets completed job IDs from provider.
// 3. Fetches job data from the API (TaskTitle, CreatedDate).
// 4. Sorts by CreatedDate (descending), TaskTitle, TaskID.
func GetJobs(ctx context.Context, provider Provider, client *p42.Client, tenantID string, verbose bool, includeCompleted bool) ([]*Job, error) {
	seen := make(map[string]bool)
	var jobs []*Job

	runningJobIDs, err := provider.GetRunningJobIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch running job IDs: %w", err)
	}

	for _, id := range runningJobIDs {
		taskID, turnIndex, parseErr := parseJobID(id)
		if parseErr != nil {
			continue
		}
		seen[id] = true
		jobs = append(jobs, &Job{
			TaskID:    taskID,
			TurnIndex: turnIndex,
			Running:   true,
		})
	}

	if includeCompleted {
		allJobIDs, allErr := provider.GetAllJobIDs(ctx)
		if allErr != nil {
			return nil, fmt.Errorf("failed to fetch all job IDs: %w", allErr)
		}

		for _, id := range allJobIDs {
			if seen[id] {
				continue
			}

			taskID, turnIndex, parseErr := parseJobID(id)
			if parseErr != nil {
				continue
			}

			seen[id] = true
			jobs = append(jobs, &Job{
				TaskID:    taskID,
				TurnIndex: turnIndex,
				Running:   false,
			})
		}
	}

	fetchJobs(ctx, jobs, client, tenantID, verbose)
	sortJobs(jobs)

	return jobs, nil
}
