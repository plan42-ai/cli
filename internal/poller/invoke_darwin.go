package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"

	"github.com/google/uuid"
	"github.com/plan42-ai/cli/internal/docker"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/log"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

func (p *pollerInvokeAgentRequest) validateTaskID() error {
	_, err := uuid.Parse(p.Turn.TaskID)
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

func (p *pollerInvokeAgentRequest) Process(ctx context.Context) messages.Message {
	// The TaskID amd DockerImage are injected into command line arguments, so we validate them before
	// we use them.
	err := p.validateTaskID()
	if err != nil {
		return agentResponse(err)
	}

	err = p.validateDockerImage()

	if err != nil {
		return agentResponse(err)
	}

	ctx = log.WithContextAttrs(ctx, slog.String("task_id", p.Turn.TaskID), slog.Int("turn_index", p.Turn.TurnIndex))

	containerID := fmt.Sprintf("plan42-%v-%v", p.Turn.TaskID, p.Turn.TurnIndex)
	slog.InfoContext(ctx, "starting agent")
	go p.runContainer(ctx, containerID)
	return &messages.InvokeAgentResponse{}
}

func (p *pollerInvokeAgentRequest) runContainer(ctx context.Context, containerID string) {
	jsonBytes, err := json.Marshal(p)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal json: %v", "error", err)
		return
	}

	// #nosec: G204: Subprocess launched with a potential tainted input or cmd arguments.
	//    This is ok. There are 2 potentially tainted inputs:
	//        - p.Environment.DockerImage:
	//              We validate that this is a docker image url before calling this function
	//        - containerID:
	//              This is equal to "plan42/<task_id>/<turn_index>". We validate that task_is is a UUID
	//              before calling this function, and turn_index is an integer value, which is verified
	//              during JSON unmarshaling.
	cmd := exec.CommandContext(
		ctx,
		"container",
		"run",
		"-c", "4",
		"-m", "8G",
		"--name", containerID,
		"-i",
		"--entrypoint", "/usr/bin/agent-wrapper",
		"--rm",
		p.Environment.DockerImage,
		"--encrypted-input=false",
		"--plan42-proxy",
	)
	cmd.Stdin = bytes.NewReader(jsonBytes)
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	err = cmd.Run()

	if err != nil {
		slog.ErrorContext(ctx, "container run failed", "error", err)
		return
	}
}

func (p *pollerInvokeAgentRequest) validateDockerImage() error {
	_, err := docker.ParseImageURI(p.Environment.DockerImage)
	if err != nil {
		return fmt.Errorf("invalid Docker image: %v", err)
	}
	return nil
}
