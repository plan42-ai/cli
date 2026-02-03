package p42runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/plan42-ai/sdk-go/p42"
)

type stubProvider struct {
	runningIDs []string
	allIDs     []string
}

func (p *stubProvider) Name() string {
	return "stub"
}

func (p *stubProvider) IsInstalled() bool {
	return true
}

func (p *stubProvider) PullImage(_ context.Context, _ string) error {
	return nil
}

func (p *stubProvider) RunJob(_ context.Context, _ JobOptions) error {
	return nil
}

func (p *stubProvider) KillJob(_ context.Context, _ string) error {
	return nil
}

func (p *stubProvider) GetRunningJobIDs(_ context.Context) ([]string, error) {
	return p.runningIDs, nil
}

func (p *stubProvider) GetAllJobIDs(_ context.Context) ([]string, error) {
	return p.allIDs, nil
}

func (p *stubProvider) ValidateJobID(_ string) error {
	return nil
}

func (p *stubProvider) DeleteJobLog(_ string) error {
	return nil
}

func newTestClient(t *testing.T, tenantID string, taskData map[string]p42.Task, turnData map[string]map[int]p42.Turn) *p42.Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(segments) == 5 && segments[0] == "v1" && segments[1] == "tenants" && segments[2] == tenantID && segments[3] == "tasks" {
			taskID := segments[4]
			task, ok := taskData[taskID]
			if !ok {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(task)
			return
		}

		if len(segments) == 7 && segments[0] == "v1" && segments[1] == "tenants" && segments[2] == tenantID && segments[3] == "tasks" && segments[5] == "turns" {
			taskID := segments[4]
			turnIndex, err := strconv.Atoi(segments[6])
			if err != nil {
				http.Error(w, "invalid turn index", http.StatusBadRequest)
				return
			}

			turn, ok := turnData[taskID][turnIndex]
			if !ok {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(turn)
			return
		}

		http.NotFound(w, r)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return p42.NewClient(server.URL)
}

func buildJobData(jobIDs []string, tenantID string, baseTime time.Time) (map[string]p42.Task, map[string]map[int]p42.Turn, error) {
	tasks := make(map[string]p42.Task)
	turns := make(map[string]map[int]p42.Turn)

	for idx, id := range jobIDs {
		taskID, turnIndex, err := parseJobID(id)
		if err != nil {
			return nil, nil, err
		}

		createdAt := baseTime.Add(time.Duration(idx) * time.Hour)
		tasks[taskID] = p42.Task{
			TenantID:  tenantID,
			TaskID:    taskID,
			Title:     fmt.Sprintf("Task %s", taskID),
			Prompt:    "",
			Parallel:  false,
			RepoInfo:  map[string]*p42.RepoInfo{},
			State:     p42.TaskStatePending,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
			Deleted:   false,
			Version:   1,
		}

		if _, ok := turns[taskID]; !ok {
			turns[taskID] = make(map[int]p42.Turn)
		}

		turns[taskID][turnIndex] = p42.Turn{
			TenantID:   tenantID,
			TaskID:     taskID,
			TurnIndex:  turnIndex,
			Prompt:     "",
			CommitInfo: map[string]p42.CommitInfo{},
			Status:     "",
			CreatedAt:  createdAt,
			UpdatedAt:  createdAt,
			Version:    1,
		}
	}

	return tasks, turns, nil
}

func TestGetJobsIncludesCompletedWithoutDuplication(t *testing.T) {
	tenantID := "tenant-123"
	running := []string{"plan42-alpha-1", "plan42-beta-2"}
	all := []string{"plan42-alpha-1", "plan42-beta-2", "plan42-gamma-3"}

	baseTime := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC)
	tasks, turns, err := buildJobData(all, tenantID, baseTime)
	if err != nil {
		t.Fatalf("unexpected build job data error: %v", err)
	}

	provider := &stubProvider{runningIDs: running, allIDs: all}
	client := newTestClient(t, tenantID, tasks, turns)

	jobs, err := GetJobs(context.Background(), provider, client, tenantID, false, true)
	if err != nil {
		t.Fatalf("GetJobs returned error: %v", err)
	}

	if len(jobs) != len(all) {
		t.Fatalf("expected %d jobs, got %d", len(all), len(jobs))
	}

	expectedRunning := map[string]bool{"alpha": true, "beta": true, "gamma": false}
	seen := make(map[string]bool)

	for _, job := range jobs {
		key := fmt.Sprintf("%s-%d", job.TaskID, job.TurnIndex)
		if seen[key] {
			t.Fatalf("duplicate job found: %s", key)
		}
		seen[key] = true

		runningExpected, ok := expectedRunning[job.TaskID]
		if !ok {
			t.Fatalf("unexpected job: %s", job.TaskID)
		}

		if job.Running != runningExpected {
			t.Errorf("job %s running = %t, expected %t", job.TaskID, job.Running, runningExpected)
		}

		turn := turns[job.TaskID][job.TurnIndex]
		if !job.CreatedDate.Equal(turn.CreatedAt) {
			t.Errorf("job %s created date %v, expected %v", job.TaskID, job.CreatedDate, turn.CreatedAt)
		}

		if job.TaskTitle != tasks[job.TaskID].Title {
			t.Errorf("job %s title %q, expected %q", job.TaskID, job.TaskTitle, tasks[job.TaskID].Title)
		}
	}
}

func TestGetJobsRunningOnly(t *testing.T) {
	tenantID := "tenant-123"
	running := []string{"plan42-delta-1", "plan42-epsilon-2"}

	baseTime := time.Date(2024, time.February, 1, 8, 0, 0, 0, time.UTC)
	tasks, turns, err := buildJobData(running, tenantID, baseTime)
	if err != nil {
		t.Fatalf("unexpected build job data error: %v", err)
	}

	provider := &stubProvider{runningIDs: running, allIDs: running}
	client := newTestClient(t, tenantID, tasks, turns)

	jobs, err := GetJobs(context.Background(), provider, client, tenantID, false, false)
	if err != nil {
		t.Fatalf("GetJobs returned error: %v", err)
	}

	if len(jobs) != len(running) {
		t.Fatalf("expected %d jobs, got %d", len(running), len(jobs))
	}

	for _, job := range jobs {
		if !job.Running {
			t.Errorf("job %s should be marked running", job.TaskID)
		}
	}
}
