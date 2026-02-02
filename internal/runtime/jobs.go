package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
)

const jobDetailConcurrency = 10

// PopulateJobDetails fetches task metadata for the provided jobs.
func PopulateJobDetails(ctx context.Context, client *p42.Client, tenantID string, jobs []*Job, verbose bool) {
	jobCh := make(chan *Job, jobDetailConcurrency)
	var wg sync.WaitGroup

	for i := 0; i < jobDetailConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				populateJob(ctx, client, tenantID, job, verbose)
			}
		}()
	}

	for _, job := range jobs {
		jobCh <- job
	}

	close(jobCh)
	wg.Wait()
}

func populateJob(ctx context.Context, client *p42.Client, tenantID string, job *Job, verbose bool) {
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
		return
	}
	job.CreatedDate = turn.CreatedAt
}

// SortJobs orders jobs by creation date, then title, then task ID.
func SortJobs(jobs []*Job) {
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

// FilterJobs removes completed jobs when includeCompleted is false.
func FilterJobs(jobs []*Job, includeCompleted bool) []*Job {
	if includeCompleted {
		return jobs
	}

	filtered := make([]*Job, 0, len(jobs))
	for _, job := range jobs {
		if job.Running {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

// FormatJobID returns the container/job name for a job.
func FormatJobID(job *Job) string {
	return fmt.Sprintf("%s%s-%d", JobNamePrefix, job.TaskID, job.TurnIndex)
}
