// nolint:revive
package util

import (
	"io"
	"os"
	"path"
)

func Pointer[T any](v T) *T {
	return &v
}

type ExitCode int

func HandleExit() {
	if r := recover(); r != nil {
		if ec, ok := r.(ExitCode); ok {
			os.Exit(int(ec))
			return
		}
		panic(r)
	}
}

func Close[T io.Closer](x T) {
	_ = x.Close()
}

func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func DefaultRunnerConfigFileName() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return path.Join(home, ".config", "plan42-runner.toml"), nil
}

func ExecutableDir() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return path.Dir(execPath), nil
}
