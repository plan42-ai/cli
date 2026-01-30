package runner

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
	"github.com/plan42-ai/cli/internal/config"
	"github.com/plan42-ai/cli/internal/poller"
	"github.com/plan42-ai/cli/internal/runtime"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
)

type Options struct {
	PlatformOptions
	Ctx           context.Context               `kong:"-"`
	Client        *p42.Client                   `kong:"-"`
	Config        config.Config                 `kong:"-"`
	ConfigFile    string                        `help:"Path to config file. Defaults to ~/.config/plan42-runner.toml" short:"c" optional:""`
	ConnectionIdx map[string]*config.GithubInfo `kong:"-"` // indexes github config based on connection id.
}

func (o *Options) PollerOptions() []poller.Option {
	ret := []poller.Option{
		poller.WithConnectionIdx(o.ConnectionIdx),
	}
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

	// Validate runtime is installed before accepting work
	provider, err := runtime.NewProvider(o.Config.Runner.Runtime)
	if err != nil {
		return err
	}
	if err := provider.Validate(context.Background()); err != nil {
		return fmt.Errorf("%s runtime validation failed: %w", provider.Name(), err)
	}

	clientOptions := []p42.Option{
		p42.WithAPIToken(o.Config.Runner.RunnerToken),
	}

	if o.Config.Runner.SkipSSLVerify {
		clientOptions = append(clientOptions, p42.WithInsecureSkipVerify())
	}

	o.Ctx = context.Background()
	o.Client = p42.NewClient(o.Config.Runner.URL, clientOptions...)
	o.ConnectionIdx = make(map[string]*config.GithubInfo)

	for _, cnn := range o.Config.Github {
		o.ConnectionIdx[cnn.ConnectionID] = cnn
	}

	err = o.Init(o.Ctx)
	if err != nil {
		return fmt.Errorf("failed to start platform services: %w", err)
	}

	return nil
}
