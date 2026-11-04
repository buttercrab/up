//go:build !darwin && !linux

package process

import (
	"context"
	"errors"
	"io"
)

func (*TerminalManager) Resize(int, int) {}
func (*TerminalManager) Run(context.Context, Command, io.Writer) error {
	return errors.New("interactive execution requires macOS or Linux")
}
func (*TerminalManager) Attach(context.Context, *TerminalSession, io.Reader, io.Writer) error {
	return errors.New("interactive execution requires macOS or Linux")
}
