package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/shlex"
	"github.com/mattn/go-isatty"
	"github.com/pelletier/go-toml/v2"
	"github.com/plan42-ai/cli/internal/apple/container"
	"github.com/plan42-ai/cli/internal/cli/runner"
	runner_config "github.com/plan42-ai/cli/internal/cli/runnerconfig"
	"github.com/plan42-ai/cli/internal/config"
	"github.com/plan42-ai/cli/internal/launchctl"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/openid/jwt"
	"github.com/plan42-ai/sdk-go/p42"
)

var (
	Version                = "dev"
	ErrRunnerNotConfigured = errors.New("runner not configured. Run `plan42 runner configure` first, then re-run `plan42 runner enable`")
)

const (
	darwin          = "darwin"
	jobIDColumn     = "Job ID"
	titleColumn     = "Title"
	turnIndexColumn = "Turn Index"
	runningColumn   = "Running?"
	createdColumn   = "Created"
)

type RunnerOptions struct {
	Config  RunnerConfigOptions  `cmd:"" help:"Edit the remote runner service config file."`
	Enable  RunnerEnableOptions  `cmd:"" help:"Enable the plan42 runner on login and start the service."`
	Exec    RunnerExecOptions    `cmd:"" help:"Execute the plan42 remote runner service."`
	Stop    RunnerStopOptions    `cmd:"" help:"Stop the plan42 runner service."`
	Status  RunnerStatusOptions  `cmd:"" help:"Show the status of the plan42 runner service."`
	Logs    RunnerLogsOptions    `cmd:"" help:"Show the logs of the plan42 runner service."`
	Disable RunnerDisableOptions `cmd:"" help:"Disable the plan42 runner service."`
	Job     RunnerJobOptions     `cmd:"" help:"Commands related to managing runner jobs."`
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
		Name: container.RunnerAgentLabel,
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
	_ = agent.Enable()
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
		Name: container.RunnerAgentLabel,
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
		Name: container.RunnerAgentLabel,
	}
	output, err := agent.Status()

	if err != nil {
		return fmt.Errorf("failed to get runner status: %w", err)
	}
	fmt.Print(output)
	return nil
}

type RunnerLogsOptions struct {
	Follow bool `name:"f" short:"f" help:"Follow log output."`
}

func (rl *RunnerLogsOptions) Run() error {
	if runtime.GOOS != darwin {
		return fmt.Errorf("runner logs not supported on %s", runtime.GOOS)
	}

	agent := launchctl.Agent{Name: container.RunnerAgentLabel}
	logPath, err := agent.LogPath()
	if err != nil {
		return fmt.Errorf("failed to determine log path: %w", err)
	}

	return viewLogFile(logPath, rl.Follow)
}

func viewLogFile(logPath string, follow bool) error {
	var logCmd *exec.Cmd
	if follow {
		logCmd = exec.Command("tail", "-f", logPath)
	} else {
		logCmd = exec.Command("cat", logPath)
	}

	logCmd.Stderr = os.Stderr

	if follow || !isatty.IsTerminal(os.Stdout.Fd()) {
		logCmd.Stdout = os.Stdout
		return logCmd.Run()
	}

	pager := os.Getenv("PAGER")
	if strings.TrimSpace(pager) == "" {
		pager = "less"
	}

	pagerArgs, err := shlex.Split(pager)
	if err != nil {
		return fmt.Errorf("failed to parse pager command: %w", err)
	}

	if len(pagerArgs) == 0 {
		pagerArgs = []string{pager}
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create pager pipe: %w", err)
	}

	// #nosec: G204 : Subprocess launched with a potential tainted input or cmd arguments
	// #    This is on purpose. We execute the PAGER that it configured in the users environment.
	pagerCmd := exec.Command(pagerArgs[0], pagerArgs[1:]...)
	pagerCmd.Stdin = reader
	pagerCmd.Stdout = os.Stdout
	pagerCmd.Stderr = os.Stderr

	err = pagerCmd.Start()
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("failed to start pager: %w", err)
	}
	_ = reader.Close()

	logCmd.Stdout = writer

	err = logCmd.Start()
	if err != nil {
		_ = writer.Close()
		pagerErr := pagerCmd.Wait()
		return errors.Join(fmt.Errorf("failed to start log command: %w", err), pagerErr)
	}

	logErr := logCmd.Wait()
	_ = writer.Close()

	pagerErr := pagerCmd.Wait()

	if logErr != nil || pagerErr != nil {
		return errors.Join(logErr, pagerErr)
	}

	return nil
}

type RunnerDisableOptions struct {
}

func (rl *RunnerDisableOptions) Run() error {
	if runtime.GOOS != darwin {
		return fmt.Errorf("runner disable not supported on %s", runtime.GOOS)
	}

	agent := launchctl.Agent{Name: container.RunnerAgentLabel}
	err := agent.Shutdown()
	if err != nil {
		return fmt.Errorf("failed to stop launchctl agent: %w", err)
	}

	err = agent.Disable()
	if err != nil {
		return fmt.Errorf("failed to disable launchctl agent: %w", err)
	}

	plistFileName, err := agent.PlistPath()
	if err == nil {
		_ = os.Remove(plistFileName)
	}

	return nil
}

type RunnerJobOptions struct {
	List ListRunnerJobOptions `cmd:"" help:"List local runner jobs."`
	Kill KillRunnerJobOptions `cmd:"" help:"Kill a local runner job."`
	Logs RunnerJobLogsOptions `cmd:"" help:"Show the logs of a runner job."`
}

type ListRunnerJobOptions struct {
	All        bool   `help:"When set, also list completed jobs." short:"a"`
	Verbose    bool   `help:"Output verbose error logs."`
	ConfigFile string `help:"Path to runner config file. Defaults to ~/.config/plan42-runner.toml" short:"c" optional:""`
}

func (l *ListRunnerJobOptions) Run() error {
	if l.ConfigFile == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to determine home directory: %w", err)
		}
		l.ConfigFile = filepath.Join(homeDir, ".config", "plan42-runner.toml")
	}

	_, err := os.Stat(l.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("runner config file %s does not exist. Run `plan42 runner config` to configure the runner", l.ConfigFile)
	}

	f, err := os.Open(l.ConfigFile)
	if err != nil {
		return fmt.Errorf("failed to open runner config file: %w", err)
	}
	defer f.Close()

	var cfg config.Config
	err = toml.NewDecoder(f).Decode(&cfg)
	if err != nil {
		return fmt.Errorf("failed to decode runner config file: %w", err)
	}

	if cfg.Runner.RunnerToken == "" {
		return fmt.Errorf("runner token not set in config. Run `plan42 runner config` to configure the runner")
	}

	s := strings.SplitN(cfg.Runner.RunnerToken, "_", 2)
	if len(s) != 2 || s[0] != "p42r" {
		return fmt.Errorf("invalid runner token in config. Run `plan42 runner config` to configure the runner")
	}

	token, err := jwt.Parse(s[1])
	if err != nil {
		return fmt.Errorf("invalid runner token in config. Run `plan42 runner config` to configure the runner")
	}

	tenantID := token.Payload.Subject
	client := p42.NewClient(token.Payload.Issuer.String(), p42.WithAPIToken(cfg.Runner.RunnerToken))

	jobs, err := container.GetLocalJobs(context.Background(), client, tenantID, l.Verbose, l.All)
	if err != nil {
		return fmt.Errorf("failed to list jobs: %w", err)
	}

	widths := getJobWidths(jobs)
	fmt.Printf(
		"%-*s     %-*s     %-*s     %-*s     %-*s\n",
		widths.ID,
		jobIDColumn,
		widths.Title,
		titleColumn,
		widths.TurnIndex,
		turnIndexColumn,
		widths.Running,
		runningColumn,
		widths.Created,
		createdColumn,
	)
	for _, job := range jobs {
		var createdDate string
		if !job.CreatedDate.IsZero() {
			createdDate = job.CreatedDate.Local().Format(time.DateTime)
		}
		fmt.Printf(
			"%-*s     %-*s     %-*d     %-*v     %-*s\n",
			widths.ID,
			fmt.Sprintf("plan42-%v-%d", job.TaskID, job.TurnIndex),
			widths.Title,
			job.TaskTitle,
			widths.TurnIndex,
			job.TurnIndex,
			widths.Running,
			job.Running,
			widths.Created,
			createdDate,
		)
	}
	return nil
}

type JobWidths struct {
	ID        int
	Title     int
	TurnIndex int
	Running   int
	Created   int
}

func getJobWidths(jobs []*container.Job) JobWidths {
	var ret JobWidths
	ret.Running = max(len("true"), len("false"), len(runningColumn))
	for _, job := range jobs {
		ret.ID = max(
			ret.ID,
			len(fmt.Sprintf("plan42-%v-%d", job.TaskID, job.TurnIndex)),
			len(jobIDColumn),
		)
		ret.Title = max(ret.Title, len(job.TaskTitle), len(titleColumn))
		ret.TurnIndex = max(ret.TurnIndex, len(fmt.Sprintf("%d", job.TurnIndex)), len(turnIndexColumn))
		ret.Created = max(ret.Created, len(job.CreatedDate.Format(time.DateTime)), len(createdColumn))
	}
	return ret
}

type RunnerJobLogsOptions struct {
	JobID  string `arg:"" name:"jobid" help:"Runner job ID to view logs for."`
	Follow bool   `name:"f" short:"f" help:"Follow log output."`
}

func (rl *RunnerJobLogsOptions) Run() error {
	if runtime.GOOS != darwin {
		return fmt.Errorf("runner job logs not supported on %s", runtime.GOOS)
	}

	logPath, err := runnerJobLogPath(rl.JobID)
	if err != nil {
		return err
	}

	return viewLogFile(logPath, rl.Follow)
}

func runnerJobLogPath(jobID string) (string, error) {
	if strings.TrimSpace(jobID) == "" {
		return "", fmt.Errorf("jobid is required")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine user home directory: %w", err)
	}

	return filepath.Join(homeDir, "Library", "Logs", container.RunnerAgentLabel, jobID), nil
}

type KillRunnerJobOptions struct {
	JobID string `arg:"" help:"The job id to kill."`
}

func (k *KillRunnerJobOptions) Run() error {
	if runtime.GOOS != darwin {
		return fmt.Errorf("runner job kill not supported on %s", runtime.GOOS)
	}

	err := container.ValidateJobID(k.JobID)
	if err != nil {
		return err
	}

	return container.KillJob(k.JobID)
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
	case "runner disable":
		err = options.Runner.Disable.Run()
	case "runner job list":
		err = options.Runner.Job.List.Run()
	case "runner job kill <job-id>":
		err = options.Runner.Job.Kill.Run()
	case "runner job logs <jobid>":
		err = options.Runner.Job.Logs.Run()
	default:
		err = fmt.Errorf("unknown command: %s", kongCtx.Command())
	}

	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		panic(util.ExitCode(-1))
	}
}
