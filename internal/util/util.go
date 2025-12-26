// nolint:revive
package util

import (
	"io"
	"os"
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
