package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/plan42-ai/cli/internal/config"
	"github.com/plan42-ai/cli/internal/poller"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
)

type Options struct {
	PlatformOptions
	Ctx        context.Context `kong:"-"`
	Client     *p42.Client     `kong:"-"`
	Loader     *config.Loader  `kong:"-"`
	ConfigFile string          `help:"Path to config file. Defaults to ~/.config/plan42-runner.toml" short:"c" optional:""`
}

func (o *Options) Close() error {
	if o.Loader != nil {
		return o.Loader.Close()
	}
	return nil
}

func (o *Options) PollerOptions() []poller.Option {
	var ret []poller.Option
	ret = o.PlatformOptions.PollerOptions(ret)
	return ret
}

func (o *Options) Process() error {
	var err error
	if o.ConfigFile == "" {
		o.ConfigFile, err = util.DefaultRunnerConfigFileName()
		if err != nil {
			return fmt.Errorf("failed to determine default config file path: %w", err)
		}
	}

	o.Loader, err = config.NewLoader(o.ConfigFile)
	if err != nil {
		return fmt.Errorf("failed to load config file: %w", err)
	}

	cfg := o.Loader.Current()

	if cfg.Runner.RunnerToken == "" {
		return errors.New("runner token not specified")
	}

	if cfg.Runner.URL == "" {
		return errors.New("endpoint URL not specified")
	}

	clientOptions := []p42.Option{
		p42.WithAPIToken(cfg.Runner.RunnerToken),
	}

	if cfg.Runner.URL == "https://localhost:7443" {
		clientOptions = append(clientOptions, p42.WithInsecureSkipVerify())
	}

	o.Ctx = context.Background()
	o.Client = p42.NewClient(cfg.Runner.URL, clientOptions...)

	return nil
}
