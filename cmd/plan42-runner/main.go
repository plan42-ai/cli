package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/plan42-ai/cli/internal/cli/runner"
	"github.com/plan42-ai/cli/internal/pollers/environments"
	"github.com/plan42-ai/cli/internal/pollers/githubevents"
	"github.com/plan42-ai/cli/internal/pollers/messages"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/clock"
	"github.com/plan42-ai/github-event-handlers/handlers"
	"github.com/plan42-ai/log"
	"github.com/plan42-ai/openid/jwt"
)

func main() {
	defer util.HandleExit()
	log.SetupTextLogging()
	var options runner.Options
	kong.Parse(&options)
	err := options.Process()
	if err != nil {
		slog.Error("error processing options", "error", err)
		panic(util.ExitCode(1))
	}
	tokenID, runnerID, err := extractParamsFromToken(options.Config.Runner.RunnerToken)
	if err != nil {
		slog.Error("error extracting params from token", "error", err)
		panic(util.ExitCode(2))
	}

	// 1. Checkpoint store: persists polling state across restarts.
	cpPath, err := githubevents.DefaultCheckpointPath()
	if err != nil {
		slog.Error("failed to determine checkpoint path", "error", err)
		panic(util.ExitCode(3))
	}
	cp, err := githubevents.NewCheckpointStore(cpPath, clock.NewRealClock())
	if err != nil {
		slog.Error("failed to create checkpoint store", "error", err)
		panic(util.ExitCode(3))
	}

	// 2. Poller: drives the GitHub Events API, parses events, and produces
	// them on a channel. Polling goroutines start lazily as targets arrive.
	poller := githubevents.NewPoller(githubevents.Config{Checkpoints: cp})

	// 3. Handler registry: routes parsed events to the appropriate handler.
	registry := handlers.NewHandlerRegistry(handlers.Config{
		Plan42Client:      options.Client,
		CommentTriggerStr: "/Plan42",
		UIURL:             options.Config.Runner.URL,
	})

	// 4. Dispatcher: consumes the poller's event channel via a worker pool.
	// Passing 0 workers defaults to 100.
	dispatcher := githubevents.NewDispatcher(registry, poller.EventCh(), 0)

	// 5. Environment discovery: reconciles the poller's target set by
	// periodically querying the Plan42 API. Starts its loop immediately.
	envPoller := environments.New(environments.Config{
		Client:        options.Client,
		TenantID:      tokenID,
		RunnerID:      runnerID,
		EventPoller:   poller,
		ConnectionIdx: options.ConnectionIdx,
	})

	msgPoller := messages.New(options.Client, tokenID, runnerID, options.PollerOptions()...)
	defer util.Close(msgPoller)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigCh

	slog.Info("Received stop signal. Shutting down.", "signal", sig.String())
	shutdownTimeout := 5 * time.Minute
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// Shutdown sequence (producer -> consumer order):

	// 1. Stop environment discovery first so no new targets are reconciled.
	if err := envPoller.ShutdownContext(shutdownCtx); err != nil {
		slog.Error("environment discovery shutdown error", "error", err)
	} else {
		slog.Info("environment discovery stopped")
	}

	// 2. Stop the github events poller: cancels polling goroutines, closes
	// EventCh(), and flushes checkpoints.
	if err := poller.Close(); err != nil {
		slog.Error("github events poller shutdown error", "error", err)
	} else {
		slog.Info("github events poller stopped")
	}

	// 3. Drain the dispatcher: workers finish processing any queued events.
	// Fall back to Close() for force-cancel if the drain times out.
	if err := dispatcher.ShutdownContext(shutdownCtx); err != nil {
		slog.Error("dispatcher graceful drain timed out, forcing shutdown", "error", err)
		dispatcher.Close()
	} else {
		slog.Info("event dispatcher stopped")
	}

	// 4. Stop the message poller.
	slog.Info("Draining message queues.")
	err = msgPoller.ShutdownTimeout(shutdownTimeout)
	if err != nil {
		slog.Error("draining message queues timed out, running force shutdown", "error", err)
	} else {
		slog.Info("message queues drained successfully, shutting down")
	}
}

func extractParamsFromToken(token string) (tokenID string, runnerID string, err error) {
	s := strings.SplitN(token, "_", 2)
	if len(s) != 2 {
		return "", "", errors.New("invalid api token")
	}
	if s[0] != "p42r" {
		return "", "", errors.New("api token is not a runner token")
	}
	parsedToken, err := jwt.Parse(s[1])
	if err != nil {
		return "", "", fmt.Errorf("invalid api token: %w", err)
	}
	if parsedToken.Payload.RunnerID == nil {
		return "", "", errors.New("invalid api token")
	}
	return parsedToken.Payload.Subject, *parsedToken.Payload.RunnerID, nil
}
