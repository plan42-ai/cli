package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/pelletier/go-toml/v2"
	"github.com/plan42-ai/cli/internal/cli/runner"
	runner_config "github.com/plan42-ai/cli/internal/cli/runnerconfig"
	"github.com/plan42-ai/cli/internal/config"
	"github.com/plan42-ai/cli/internal/launchctl"
	"github.com/plan42-ai/cli/internal/util"
)

var (
	Version                = "dev"
	ErrRunnerNotConfigured = errors.New("runner not configured. Run `plan42 runner configure` first, then re-run `plan42 runner enable`")
)

const (
	runnerAgentLabel = "ai.plan42.runner"
	darwin           = "darwin"
)

type RunnerOptions struct {
	Config RunnerConfigOptions `cmd:"" help:"Edit the remote runner service config file."`
	Enable RunnerEnableOptions `cmd:"" help:"Enable the plan42 runner on login and start the service."`
	Exec   RunnerExecOptions   `cmd:"" help:"Execute the plan42 remote runner service."`
	Stop   RunnerStopOptions   `cmd:"" help:"Stop the plan42 runner service."`
	Status RunnerStatusOptions `cmd:"" help:"Show the status of the plan42 runner service."`
	Logs   RunnerLogsOption    `cmd:"" help:"Show the logs of the plan42 runner service."`
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

type RunnerExecOptions struct {
	runner.Options
}

func (r *RunnerExecOptions) Run() error {
	return forwardToSibling("plan42-runner", 3)
}

type RunnerEnableOptions struct {
	ConfigFile string `help:"Path to config file. Defaults to ~/.config/plan42-runner.toml" short:"c" optional:""`
}

func (r *RunnerEnableOptions) Run() error {
	if runtime.GOOS != darwin {
		return fmt.Errorf("runner enable not supported on %s", runtime.GOOS)
	}

	configPath, err := r.resolveConfigPath()
	if err != nil {
		return err
	}

	err = validateRunnerConfig(configPath)
	if err != nil {
		return err
	}

	return r.enableLaunchAgent(configPath)
}

func (r *RunnerEnableOptions) resolveConfigPath() (string, error) {
	configPath := r.ConfigFile
	if configPath == "" {
		var err error
		configPath, err = util.DefaultRunnerConfigFileName()
		if err != nil {
			return "", fmt.Errorf("unable to determine default config file: %w", err)
		}
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve config file path: %w", err)
	}

	return absPath, nil
}

func validateRunnerConfig(configPath string) error {
	f, err := os.Open(configPath)
	if err != nil {
		return ErrRunnerNotConfigured
	}
	defer util.Close(f)

	decoder := toml.NewDecoder(f)
	var cfg config.Config
	err = decoder.Decode(&cfg)
	if err != nil {
		return ErrRunnerNotConfigured
	}

	if cfg.Runner.RunnerToken == "" || cfg.Runner.URL == "" {
		return ErrRunnerNotConfigured
	}

	return nil
}

func (r *RunnerEnableOptions) enableLaunchAgent(configPath string) error {
	execDir, err := util.ExecutableDir()
	if err != nil {
		return fmt.Errorf("unable to determine executable directory: %w", err)
	}
	runnerPath := filepath.Join(execDir, "plan42-runner")

	containerPath, err := exec.LookPath("container")
	if err != nil {
		return fmt.Errorf("unable to find `container` on path: %w", err)
	}

	_, err = os.Stat(runnerPath)
	if err != nil {
		return fmt.Errorf("unable to locate plan42-runner executable: %w", err)
	}

	agent := launchctl.Agent{
		Name: runnerAgentLabel,
		Argv: []string{
			runnerPath,
			"--config-file",
			configPath,
			"--container-path",
			containerPath,
		},
		ExitTimeout: util.Pointer(5 * time.Minute),
		CreateLog:   true,
	}
	err = agent.Create()
	if err != nil {
		return err
	}

	_ = agent.Shutdown()
	err = agent.Bootstrap()
	if err != nil {
		return fmt.Errorf("failed to bootstrap launchctl agent: %w", err)
	}

	err = agent.Kickstart()

	if err != nil {
		return fmt.Errorf("failed to start launchctl agent: %w", err)
	}

	return nil
}

type RunnerConfigOptions struct {
	runner_config.Options
}

func (rc *RunnerConfigOptions) Run() error {
	return forwardToSibling("plan42-runner-config", 3)
}

type RunnerStopOptions struct{}

func (rs *RunnerStopOptions) Run() error {
	if runtime.GOOS != darwin {
		return fmt.Errorf("runner stop not supported on %s", runtime.GOOS)
	}

	agent := launchctl.Agent{
		Name: runnerAgentLabel,
	}
	err := agent.Shutdown()
	if err != nil {
		return fmt.Errorf("failed to stop launchctl agent: %w", err)
	}
	return nil
}

type RunnerStatusOptions struct{}

func (rs *RunnerStatusOptions) Run() error {
	if runtime.GOOS != darwin {
		return fmt.Errorf("runner status not supported on %s", runtime.GOOS)
	}
	agent := launchctl.Agent{
		Name: runnerAgentLabel,
	}
	output, err := agent.Status()

	if err != nil {
		return fmt.Errorf("failed to get runner status: %w", err)
	}
	fmt.Print(output)
	return nil
}

type RunnerLogsOption struct {
	Follow bool `name:"f" short:"f" help:"Follow log output."`
}

func (rl *RunnerLogsOption) Run() error {
	return errors.New("not implemented")
}

type Options struct {
	Version kong.VersionFlag `help:"Print version and exit" name:"version" short:"v"`
	Runner  RunnerOptions    `cmd:""`
}

func main() {
	defer util.HandleExit()
	var options Options
	kongCtx := kong.Parse(
		&options,
		kong.Vars{"version": Version},
	)

	var err error
	switch kongCtx.Command() {
	case "runner exec":
		err = options.Runner.Exec.Run()
	case "runner enable":
		err = options.Runner.Enable.Run()
	case "runner config":
		err = options.Runner.Config.Run()
	case "runner stop":
		err = options.Runner.Stop.Run()
	case "runner status":
		err = options.Runner.Status.Run()
	case "runner logs":
		err = options.Runner.Logs.Run()
	default:
		err = fmt.Errorf("unknown command: %s", kongCtx.Command())
	}

	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		panic(util.ExitCode(-1))
	}
}
