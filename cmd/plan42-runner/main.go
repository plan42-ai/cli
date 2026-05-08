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
	pollerGithubEvents "github.com/plan42-ai/cli/internal/pollers/githubevents"
	"github.com/plan42-ai/cli/internal/pollers/messages"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/concurrency"
	githubevents "github.com/plan42-ai/github-event-handlers"
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

	// Create the shared HandlerRegistry for github event handlers.
	// The runner uses the Plan42 client (which satisfies Plan42Client) and
	// does not need GithubAppName/GithubAppID (those are webhook-only).
	registry := githubevents.NewHandlerRegistry(githubevents.Config{
		Plan42Client:      options.Client,
		CommentTriggerStr: "/Plan42",
		UIURL:             options.Config.Runner.URL,
	})

	// Create the checkpoint store for persisting polling state across restarts.
	checkpointCg := concurrency.NewContextGroup()
	checkpointStore, err := pollerGithubEvents.NewCheckpointStore(checkpointCg)
	if err != nil {
		slog.Error("error creating checkpoint store", "error", err)
		panic(util.ExitCode(3))
	}

	// Create and start the github events poller (owns per-pair polling goroutines
	// and the dispatcher worker pool).
	eventsPoller := pollerGithubEvents.New(pollerGithubEvents.Config{
		Registry:    registry,
		Checkpoints: checkpointStore,
	})
	eventsPoller.Start()

	// Create and start environment discovery (discovers which (connection, org)
	// pairs to poll and calls Reconcile on the events poller).
	envPoller := environments.New(environments.Config{
		Client:     options.Client,
		TenantID:   tokenID,
		RunnerID:   runnerID,
		Reconciler: eventsPoller,
	})
	envPoller.Start()

	// Start the message poller (existing behavior: polls runner queues for work).
	msgPoller := messages.New(options.Client, tokenID, runnerID, options.PollerOptions()...)
	defer util.Close(msgPoller)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	slog.Info("Received stop signal. Shutting down.", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer shutdownCancel()

	// Shutdown order matters:
	// 1. Stop environment discovery (no new Reconcile calls).
	if err := envPoller.ShutdownContext(shutdownCtx); err != nil {
		slog.Error("environment discovery shutdown error", "error", err)
	}
	// 2. Stop the github events poller (cancels polling goroutines, drains dispatcher).
	if err := eventsPoller.ShutdownContext(shutdownCtx); err != nil {
		slog.Error("github events poller shutdown error", "error", err)
	}
	// 3. Flush checkpoint store (persists any pending in-memory changes).
	if err := checkpointStore.Flush(shutdownCtx); err != nil {
		slog.Error("checkpoint store flush error", "error", err)
	}
	// 4. Stop the message poller (existing behavior).
	if err := msgPoller.ShutdownTimeout(5 * time.Minute); err != nil {
		slog.Error("message poller shutdown error", "error", err)
	} else {
		slog.Info("shutdown complete")
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
