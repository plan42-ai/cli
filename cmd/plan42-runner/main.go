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
	"github.com/plan42-ai/cli/internal/poller"
	"github.com/plan42-ai/cli/internal/util"
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
	p := poller.New(options.Client, tokenID, runnerID, options.PollerOptions()...)
	defer util.Close(p)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigCh

	slog.Info("Received stop signal. Draining queues. This will take 30 seconds.", "signal", sig.String())
	err = p.ShutdownTimeout(time.Minute * 5)
	if err != nil {
		slog.ErrorContext(context.Background(), "draining queues timedoout, running force shutdown", "error", err)
	} else {
		slog.Info("queues drained successfully, shutting down")
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
