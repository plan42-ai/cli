package runnerconfig

import (
	"fmt"

	"github.com/plan42-ai/cli/internal/util"
)

type Options struct {
	ConfigFile string `help:"Path to config file. Defaults to ~/.config/plan42-runner.toml" short:"c" optional:""`
}

func (o *Options) Process() error {
	var err error
	if o.ConfigFile == "" {
		o.ConfigFile, err = util.DefaultRunnerConfigFileName()
		if err != nil {
			return fmt.Errorf("failed to determine default config file path: %w", err)
		}
	}
	return nil
}
