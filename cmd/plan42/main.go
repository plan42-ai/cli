package main

import (
	"fmt"
	"os"
	"path"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/plan42-ai/cli/internal/util"
)

type RunnerExecOptions struct {
	ConfigFile string `help:"Path to config file. Defaults to ~/.config/plan42-runner.toml" short:"c" optional:""`
}

type RunnerOptions struct {
	Config RunnerConfigOptions `cmd:"" help:"Edit the remote runner service config file."`
	Exec   RunnerExecOptions   `cmd:"" help:"Execute the plan42 remote runner service."`
}

func forwardToSibling(execName string, commandDepth int) error {
	execDir, err := util.ExecutableDir()
	if err != nil {
		return fmt.Errorf("unable to determine executable directory: %w", err)
	}
	execPath := path.Join(execDir, execName)
	args := []string{
		execName,
	}
	args = append(args, os.Args[commandDepth:]...)
	err = syscall.Exec(execPath, args, os.Environ())
	if err != nil {
		return err
	}
	return nil
}

func (r *RunnerExecOptions) Run() error {
	return forwardToSibling("plan42-runner", 3)
}

type RunnerConfigOptions struct {
	ConfigFile string `help:"Path to config file. Defaults to ~/.config/plan42-runner.toml" short:"c" optional:""`
}

func (rc *RunnerConfigOptions) Run() error {
	return forwardToSibling("plan42-runner-config", 3)
}

type Options struct {
	Runner RunnerOptions `cmd:""`
}

func main() {
	defer util.HandleExit()
	var options Options
	kongCtx := kong.Parse(&options)
	var err error
	switch kongCtx.Command() {
	case "runner exec":
		err = options.Runner.Exec.Run()
	case "runner config":
		err = options.Runner.Config.Run()
	default:
		err = fmt.Errorf("unknown command: %s", kongCtx.Command())
	}

	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		panic(util.ExitCode(-1))
	}
}
