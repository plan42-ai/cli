package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"

	"github.com/google/uuid"
	"github.com/plan42-ai/cli/internal/docker"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/log"
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
	containerID := fmt.Sprintf("plan42-%v-%v", req.Turn.TaskID, req.Turn.TurnIndex)
	ctx = log.WithContextAttrs(
		ctx,
		slog.String("task_id", req.Turn.TaskID),
		slog.Int("turn_index", req.Turn.TurnIndex),
		slog.String("container_id", containerID),
	)
	slog.InfoContext(ctx, "pulling image")
	output, err := req.pulContainer(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to pull image", "err", err, "output", output)
		return agentResponse(err)
	}

	slog.InfoContext(ctx, "starting agent")
	go req.runContainer(ctx, containerID)
	return &messages.InvokeAgentResponse{}
}

func (req *pollerInvokeAgentRequest) pulContainer(ctx context.Context) (string, error) {
	// #nosec G204:  Subprocess launched with a potential tainted input or cmd arguments
	output, err := exec.CommandContext(
		ctx,
		req.ContainerPath,
		"image",
		"pull",
		req.Environment.DockerImage,
	).CombinedOutput()

	return string(output), err
}

func (req *pollerInvokeAgentRequest) runContainer(ctx context.Context, containerID string) {
	jsonBytes, err := json.Marshal(req)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal json", "error", err)
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.ErrorContext(ctx, "failed to get user home dir", "error", err)
		return
	}

	logPath := path.Join(
		homeDir,
		"Library",
		"Logs",
		"ai.plan42.runner",
		containerID,
	)

	logFile, err := os.Create(logPath)
	if err != nil {
		slog.ErrorContext(ctx, "failed to create log file", "error", err)
		return
	}
	defer util.Close(logFile)

	// #nosec: G204: Subprocess launched with a potential tainted input or cmd arguments.
	//    This is ok. There are 2 potentially tainted inputs:
	//        - req.Environment.DockerImage:
	//              We validate that this is a docker image url before calling this function
	//        - containerID:
	//              This is equal to "plan42/<task_id>/<turn_index>". We validate that task_is is a UUID
	//              before calling this function, and turn_index is an integer value, which is verified
	//              during JSON unmarshaling.
	cmd := exec.CommandContext(
		ctx,
		req.ContainerPath,
		"run",
		"-c", "4",
		"-m", "8G",
		"--name", containerID,
		"-i",
		"--entrypoint", "/usr/bin/agent-wrapper",
		"--rm",
		req.Environment.DockerImage,
		"--encrypted-input=false",
		"--plan42-proxy",
		"--log-agent-output",
	)
	cmd.Stdin = bytes.NewReader(jsonBytes)

	cmd.Stderr = logFile
	cmd.Stdout = logFile
	err = cmd.Run()

	if err != nil {
		slog.ErrorContext(ctx, "container run failed", "error", err)
		return
	}
}

func (req *pollerInvokeAgentRequest) validateDockerImage() error {
	_, err := docker.ParseImageURI(req.Environment.DockerImage)
	if err != nil {
		return fmt.Errorf("invalid Docker image: %v", err)
	}
	return nil
}

func (req *pollerInvokeAgentRequest) Init(p *Poller) {
	req.ContainerPath = p.ContainerPath
}
