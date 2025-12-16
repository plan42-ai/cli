package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/debugging-sucks/event-horizon-sdk-go/eh/messages"
	"github.com/debugging-sucks/runner/internal/docker"
	"github.com/debugging-sucks/runner/internal/log"
	"github.com/debugging-sucks/runner/internal/util"
	"github.com/google/uuid"
)

// Start an HTTP server on a Unix Domain Socket.
// We serve a single endpoint (GET /), that returns the JSON InvokeAgentRequest message.
// We inject the Unix socket into the container that gets spun up, and the agent
// will connect to it to fetch it's input. Once the handler is invoked, we close the 'done' channel
// to signal that the agent has started running, so that we can return from Process().
func (p *pollerInvokeAgentRequest) startHTTP(socketPath string, done chan struct{}) (net.Listener, error) {
	ret, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on unix socket: %w", err)
	}
	go func() {
		// #nosec: G114: Use of net/http Serve function that has no support for settings timeouts.
		//    It's theoretically possible for a malicious Plan42 API server to send a request that
		//    runs a customer docker image that attempts to dos a user's local runner.
		//    But, adding a timeout here only provides limited protection against a malicious api server,
		//    so we don't bother trying to address.
		_ = http.Serve(ret, p.handler(done))
	}()
	return ret, nil
}

func (p *pollerInvokeAgentRequest) handler(done chan struct{}) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			if r.URL.Path != "/" {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			defer close(done)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			enc := json.NewEncoder(w)
			_ = enc.Encode(p)
		},
	)
}

func (p *pollerInvokeAgentRequest) validateTaskID() error {
	_, err := uuid.Parse(p.Turn.TaskID)
	if err != nil {
		return fmt.Errorf("invalid task ID: %v", err)
	}
	return nil
}

func (p *pollerInvokeAgentRequest) Process(ctx context.Context) messages.Message {
	// The TaskID amd DockerImage are injected into command line arguments, so we validate them before
	// we use them.
	err := p.validateTaskID()
	if err != nil {
		return &messages.InvokeAgentResponse{
			ErrorMessage: util.Pointer(err.Error()),
		}
	}

	err = p.validateDockerImage()
	if err != nil {
		return &messages.InvokeAgentResponse{
			ErrorMessage: util.Pointer(err.Error()),
		}
	}

	socketPath := path.Join(os.TempDir(), fmt.Sprintf("%v.sock", uuid.NewString()))
	done := make(chan struct{})
	l, err := p.startHTTP(socketPath, done)
	if err != nil {
		return &messages.InvokeAgentResponse{
			ErrorMessage: util.Pointer(err.Error()),
		}
	}
	defer l.Close()
	defer os.Remove(socketPath)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx = log.WithContextAttrs(ctx, slog.String("task_id", p.Turn.TaskID), slog.Int("turn_index", p.Turn.TurnIndex))

	err = p.startAgent(ctx, socketPath)
	if err != nil {
		return &messages.InvokeAgentResponse{
			ErrorMessage: util.Pointer(err.Error()),
		}
	}

	err = p.waitTimeout(ctx, done)
	if err != nil {
		slog.ErrorContext(ctx, "agent did not start successfully", "error", err)
		return &messages.InvokeAgentResponse{
			ErrorMessage: util.Pointer(err.Error()),
		}
	}
	slog.InfoContext(ctx, "started agent")
	return &messages.InvokeAgentResponse{}
}

func (p *pollerInvokeAgentRequest) startAgent(ctx context.Context, path string) error {
	containerID := fmt.Sprintf("plan42-%v-%v", p.Turn.TaskID, p.Turn.TurnIndex)
	err := p.createContainer(ctx, path, containerID)
	if err != nil {
		return err
	}
	return p.startContainer(ctx, containerID)
}

func (p *pollerInvokeAgentRequest) createContainer(ctx context.Context, path string, containerID string) error {
	// #nosec: G204: Subprocess launched with a potential tainted input or cmd arguments.
	//    This is ok. There are 3 potentially tained inputs:
	//        - p.Environment.DockerImage:
	//              We validate that this is a docker image url before calling this function
	//        - containerID:
	//              This is equal to "plan42/<task_id>/<turn_index>". We validate that task_is is a UUID
	//              before calling this function, and turn_index is an integer value, which is verified
	//              during JSON unmarshaling.
	//        - path:
	//               This a temp file path, and of the form $TMPDIR/<uuid>.sock. The UUID is generated
	//               by p.Process. The temp dir env var CAN be modified by the owner of the machine we are running on,
	//               but that's "outside the scope of our threat model" (i.e. we assume the owner of the machine
	//               is trusted).
	cmd := exec.CommandContext(
		ctx,
		"container",
		"create",
		"-c", "4",
		"-m", "8G",
		"--name", containerID,
		"--publish-socket", fmt.Sprintf("%v:/tmp/agent.sock", path),
		"--rm",
		p.Environment.DockerImage,
		"--input-socket", "/tmp/agent.sock",
	)
	cmdOutput, err := cmd.CombinedOutput()
	if err != nil {
		slog.ErrorContext(ctx, "unable to create container", "error", err, "output", string(cmdOutput))
		return fmt.Errorf("failed to create agent container: %v: %v", err, string(cmdOutput))
	}
	return nil
}

func (p *pollerInvokeAgentRequest) startContainer(ctx context.Context, containerID string) error {
	// #nosec: G204: Subprocess launched with a potential tainted input or cmd arguments.
	//    This is ok. The containerID is equal to "plan42/<task_id>/<turn_index>". See the comments in createContainer
	//    for more details on why this is safe.
	cmd := exec.CommandContext(
		ctx,
		"container",
		"start",
		containerID,
	)
	cmdOutput, err := cmd.CombinedOutput()
	if err != nil {
		slog.ErrorContext(ctx, "unable to start container", "error", err, "output", string(cmdOutput))
		return fmt.Errorf("failed to start agent container: %v: %v", err, string(cmdOutput))
	}
	return nil
}

func (p *pollerInvokeAgentRequest) waitTimeout(ctx context.Context, done chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pollerInvokeAgentRequest) validateDockerImage() error {
	_, err := docker.ParseImageURI(p.Environment.DockerImage)
	if err != nil {
		return fmt.Errorf("invalid Docker image: %v", err)
	}
	return nil
}
