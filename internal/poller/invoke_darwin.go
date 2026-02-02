package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/plan42-ai/cli/internal/docker"
	containerruntime "github.com/plan42-ai/cli/internal/runtime"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/log"
	"github.com/plan42-ai/sdk-go/p42"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

func (req *pollerInvokeAgentRequest) validateTaskID() error {
	_, err := uuid.Parse(req.Turn.TaskID)
	if err != nil {
		return fmt.Errorf("invalid task ID: %v", err)
	}
	return nil
}

func agentResponse(err error) *messages.InvokeAgentResponse {
	return &messages.InvokeAgentResponse{
		ErrorMessage: util.Pointer(err.Error()),
	}
}

func (req *pollerInvokeAgentRequest) Process(ctx context.Context) messages.Message {
	// The TaskID amd DockerImage are injected into command line arguments, so we validate them before
	// we use them.
	err := req.validateTaskID()
	if err != nil {
		return agentResponse(err)
	}

	err = req.validateDockerImage()

	if err != nil {
		return agentResponse(err)
	}

	if req.runtimeProvider == nil {
		return agentResponse(errors.New("container runtime not configured"))
	}
	containerID := fmt.Sprintf("plan42-%v-%v", req.Turn.TaskID, req.Turn.TurnIndex)
	ctx = log.WithContextAttrs(
		ctx,
		slog.String("task_id", req.Turn.TaskID),
		slog.Int("turn_index", req.Turn.TurnIndex),
		slog.String("container_id", containerID),
	)
	slog.InfoContext(ctx, "received invoke request")

	go req.invokeAsync(ctx, containerID)
	return &messages.InvokeAgentResponse{}
}

func (req *pollerInvokeAgentRequest) invokeAsync(ctx context.Context, containerID string) {
	if req.shouldFetchPRFeedback() {
		if err := req.updateTurnStatus(ctx, "Checking for PR Feedback"); err != nil {
			slog.ErrorContext(ctx, "failed to update turn status", "status", "Checking for PR Feedback", "error", err)
			return
		}
		if err := req.fetchPRFeedbackIfNeeded(ctx); err != nil {
			slog.ErrorContext(ctx, "failed to fetch feedback", "error", err)
			return
		}
	}

	if err := req.updateTurnStatus(ctx, "Pulling Agent Image on Local Runner"); err != nil {
		slog.ErrorContext(ctx, "failed to update turn status", "status", "Pulling Agent Image on Local Runner", "error", err)
		return
	}

	slog.InfoContext(ctx, "pulling image")
	if err := req.pullImage(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to pull image", "err", err)
		return
	}

	slog.InfoContext(ctx, "starting agent")
	if err := req.runContainer(ctx, containerID); err != nil {
		slog.ErrorContext(ctx, "container run failed", "error", err)
	}
}

func (req *pollerInvokeAgentRequest) pullImage(ctx context.Context) error {
	return req.runtimeProvider.PullImage(ctx, req.Environment.DockerImage)
}

func (req *pollerInvokeAgentRequest) runContainer(ctx context.Context, containerID string) error {
	jsonBytes, err := json.Marshal(req)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal json", "error", err)
		return err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.ErrorContext(ctx, "failed to get user home dir", "error", err)
		return err
	}

	logPath := filepath.Join(
		homeDir,
		"Library",
		"Logs",
		containerruntime.RunnerAgentLabel,
		containerID,
	)

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		slog.ErrorContext(ctx, "failed to create log directory", "error", err)
		return err
	}

	reader := bytes.NewReader(jsonBytes)
	job := containerruntime.JobOptions{
		JobID:      containerID,
		Image:      req.Environment.DockerImage,
		CPUs:       4,
		Memory:     8 * 1024 * 1024 * 1024,
		Entrypoint: "/usr/bin/agent-wrapper",
		Args: []string{
			"--encrypted-input=false",
			"--plan42-proxy",
			"--log-agent-output",
		},
		Stdin:   reader,
		LogPath: logPath,
	}

	return req.runtimeProvider.RunJob(ctx, job)
}

func (req *pollerInvokeAgentRequest) shouldFetchPRFeedback() bool {
	if req.FeedBack != nil || req.PrivateGithubConnectionID == nil {
		return false
	}
	return req.Turn.TurnIndex > 1
}

func (req *pollerInvokeAgentRequest) updateTurnStatus(ctx context.Context, status string) error {
	updated, err := req.client.UpdateTurn(
		ctx,
		&p42.UpdateTurnRequest{
			TenantID:  req.Turn.TenantID,
			TaskID:    req.Turn.TaskID,
			TurnIndex: req.Turn.TurnIndex,
			Version:   req.Turn.Version,
			Status:    util.Pointer(status),
		},
	)
	if err != nil {
		return err
	}
	req.Turn = updated
	return nil
}

func (req *pollerInvokeAgentRequest) validateDockerImage() error {
	_, err := docker.ParseImageURI(req.Environment.DockerImage)
	if err != nil {
		return fmt.Errorf("invalid Docker image: %v", err)
	}
	return nil
}

func (req *pollerInvokeAgentRequest) fetchPRFeedbackIfNeeded(ctx context.Context) error {
	if req.FeedBack != nil || req.PrivateGithubConnectionID == nil {
		return nil
	}

	if req.githubClient == nil {
		return fmt.Errorf("github client not configured")
	}

	feedback := make(map[string][]messages.PRFeedback)

	repoInfo := map[string]*p42.RepoInfo{}
	if req.Task != nil && req.Task.RepoInfo != nil {
		repoInfo = req.Task.RepoInfo
	}

	for orgRepo, info := range repoInfo {
		if info == nil || info.PRNumber == nil {
			continue
		}
		org, repo, err := splitRepoName(orgRepo)
		if err != nil {
			return err
		}
		fb, err := req.githubClient.GetPRFeedBack(ctx, org, repo, *info.PRNumber)
		if err != nil {
			return err
		}
		feedback[orgRepo] = fb
	}

	return setFeedback(&req.FeedBack, feedback)
}

func splitRepoName(name string) (string, string, error) {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo name: %s", name)
	}
	return parts[0], parts[1], nil
}

func setFeedback(dst any, feedback map[string][]messages.PRFeedback) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Errorf("feedback destination is not settable")
	}
	v = v.Elem()
	switch v.Kind() {
	case reflect.Map:
		v.Set(reflect.ValueOf(feedback))
		return nil
	case reflect.Pointer:
		ptrVal := reflect.ValueOf(&feedback)
		if !ptrVal.Type().AssignableTo(v.Type()) {
			return fmt.Errorf("unsupported feedback pointer type")
		}
		v.Set(ptrVal)
		return nil
	default:
		return fmt.Errorf("unsupported feedback field type")
	}
}

func (req *pollerInvokeAgentRequest) Init(p *Poller) {
	req.runtimeProvider = p.runtimeProvider
	req.client = p.client.WithAPIToken(req.AgentToken)
	if req.PrivateGithubConnectionID != nil {
		cnn := p.connectionIdx[*req.PrivateGithubConnectionID]
		if cnn != nil {
			req.GithubToken = util.Pointer(cnn.Token)
			req.GithubURL = util.Pointer(cnn.URL)
		}
		client, err := p.GetClientForConnectionID(*req.PrivateGithubConnectionID)
		if err != nil {
			slog.Error("unable to initialize github client", "connection_id", *req.PrivateGithubConnectionID, "error", err)
		} else {
			req.githubClient = client
		}
	}
}
