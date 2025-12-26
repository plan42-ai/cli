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
	"github.com/debugging-sucks/event-horizon-sdk-go/eh"
	"github.com/debugging-sucks/openid/jwt"
	"github.com/debugging-sucks/runner/internal/config"
	"github.com/debugging-sucks/runner/internal/log"
	"github.com/debugging-sucks/runner/internal/poller"
	"github.com/debugging-sucks/runner/internal/util"
	"github.com/pelletier/go-toml/v2"
)

type Options struct {
	Ctx        context.Context `kong:"-"`
	Client     *eh.Client      `kong:"-"`
	Config     config.Config   `kong:"-"`
	ConfigFile string          `help:"Path to config file. Defaults to ~/.config/plan42-runner.toml" short:"c" optional:""`
}

func (o *Options) process() error {
	var err error
	if o.ConfigFile == "" {
		o.ConfigFile, err = util.DefaultRunnerConfigFileName()
		if err != nil {
			return fmt.Errorf("failed to determine default config file path: %w", err)
		}
	}

	f, err := os.Open(o.ConfigFile)
	if err != nil {
		return fmt.Errorf("failed to open config file: %w", err)
	}
	defer util.Close(f)

	decoder := toml.NewDecoder(f)
	err = decoder.Decode(&o.Config)
	if err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	if o.Config.Runner.RunnerToken == "" {
		return errors.New("runner token not specified")
	}

	if o.Config.Runner.URL == "" {
		return errors.New("endpoint URL not specified")
	}

	clientOptions := []eh.Option{
		eh.WithAPIToken(o.Config.Runner.RunnerToken),
	}

	if o.Config.Runner.URL == "https://localhost:7443" {
		clientOptions = append(clientOptions, eh.WithInsecureSkipVerify())
	}

	o.Ctx = context.Background()
	o.Client = eh.NewClient(o.Config.Runner.URL, clientOptions...)

	return nil
}

func main() {
	defer util.HandleExit()
	setupLogging()
	var options Options
	kong.Parse(&options)
	err := options.process()
	if err != nil {
		slog.Error("error processing options", "error", err)
		panic(util.ExitCode(1))
	}
	tokenID, runnerID, err := extractParamsFromToken(options.Config.Runner.RunnerToken)
	if err != nil {
		slog.Error("error extracting params from token", "error", err)
		panic(util.ExitCode(2))
	}
	p := poller.New(options.Client, tokenID, runnerID)
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

func setupLogging() {
	handler := log.NewContextHandler(slog.NewTextHandler(os.Stdout, nil))
	logger := slog.New(handler)
	slog.SetDefault(logger)
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
