package runtime

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

// GetJobs returns a fully populated, sorted list of jobs.
// It performs the following steps:
// 1. Gets running job IDs from provider.
// 2. Optionally gets completed job IDs from provider.
// 3. Fetches job data from the API (TaskTitle, CreatedDate).
// 4. Sorts by CreatedDate (descending), TaskTitle, TaskID.
func GetJobs(ctx context.Context, provider Provider, client *p42.Client, tenantID string, verbose bool, includeCompleted bool) ([]*Job, error) {
	seen := make(map[string]bool)
	jobs := make([]*Job, 0)

	// Get running jobs
	runningIDs, err := provider.GetRunningJobIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get running jobs: %w", err)
	}

	for _, id := range runningIDs {
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

	// Get completed jobs if requested
	if includeCompleted {
		completedIDs, err := provider.GetCompletedJobIDs()
		if err != nil {
			return nil, fmt.Errorf("failed to get completed jobs: %w", err)
		}

		for _, id := range completedIDs {
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

	// Fetch job data from API
	fetchJobs(ctx, jobs, client, tenantID, verbose)

	// Sort jobs
	sortJobs(jobs)

	return jobs, nil
}
