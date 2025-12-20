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
	"github.com/debugging-sucks/runner/internal/log"
	"github.com/debugging-sucks/runner/internal/poller"
	"github.com/debugging-sucks/runner/internal/util"
)

type Options struct {
	Ctx      context.Context `kong:"-"`
	Client   *eh.Client      `kong:"-"`
	APIToken string          `help:"API token" short:"t" required:"true" env:"PLAN42_API_TOKEN"`
	Endpoint string          `help:"Set to override the Plan42 api endpoint." optional:""`
	Dev      bool            `help:"Point at the dev api endpoint (api.dev.plan42.ai)." optional:""`
	Insecure bool            `help:"Don't validate the api cert." optional:""`
	Local    bool            `help:"Short for --endpoint localhost:7443 --insecure"`
}

func (o *Options) process() error {
	if o.Dev && o.Local {
		return errors.New("cannot use both --dev and --local options together")
	}

	if o.Local && o.Endpoint == "" {
		o.Endpoint = "https://localhost:7443"
	}

	if o.Dev && o.Endpoint == "" {
		o.Endpoint = "https://api.dev.plan42.ai"
	}

	if o.Endpoint == "" {
		o.Endpoint = "https://api.plan42.ai"
	}

	if o.Local {
		o.Insecure = true
	}

	var options []eh.Option
	if o.Insecure {
		options = append(options, eh.WithInsecureSkipVerify())
	}

	o.Ctx = context.Background()

	options = append(options, eh.WithAPIToken(o.APIToken))
	o.Client = eh.NewClient(o.Endpoint, options...)

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
	tokenID, runnerID, err := extractParamsFromToken(options.APIToken)
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
