//go:build darwin || linux

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

func (m *TerminalManager) Resize(cols, rows int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cols, m.rows = max(1, cols), max(1, rows)
	for _, s := range m.sessions {
		_ = pty.Setsize(s.file, &pty.Winsize{Cols: uint16(min(m.cols, 65535)), Rows: uint16(min(m.rows, 65535))})
	}
}

func (m *TerminalManager) Run(ctx context.Context, c Command, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Path == "" || c.TaskID == "" {
		return errors.New("terminal command requires a path and task ID")
	}
	if output == nil {
		output = io.Discard
	}
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir, cmd.Env = c.Dir, append(os.Environ(), c.Env...)
	cleanup, err := configureCancellation(cmd, 2*time.Second)
	if err != nil {
		return err
	}
	// A controlling terminal requires a new session, rather than Setpgid.
	// Its session leader is also the process-group leader for cancellation.
	cmd.SysProcAttr.Setpgid = false
	m.mu.Lock()
	cols, rows := m.cols, m.rows
	m.mu.Unlock()
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(min(cols, 65535)), Rows: uint16(min(rows, 65535))})
	if err != nil {
		cleanup(false)
		return fmt.Errorf("start %s: %w", c.Path, err)
	}
	s := &TerminalSession{file: master, done: make(chan struct{}), output: output}
	m.mu.Lock()
	if m.sessions == nil {
		m.sessions = make(map[string]*TerminalSession)
	}
	m.sessions[c.TaskID] = s
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.sessions, c.TaskID)
		m.mu.Unlock()
		close(s.done)
		_ = master.Close()
	}()
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(s, master)
		copyDone <- err
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var copyErr error
	select {
	case copyErr = <-copyDone:
		if !terminalEOF(copyErr) {
			cleanup(true)
		}
		err = <-waitDone
	case err = <-waitDone:
		cleanup(ctx.Err() != nil)
		select {
		case copyErr = <-copyDone:
		case <-time.After(time.Second):
			cleanup(true)
			_ = master.Close()
			copyErr = <-copyDone
			if err == nil {
				err = exec.ErrWaitDelay
			}
		}
	}
	cleanup(ctx.Err() != nil)
	if !terminalEOF(copyErr) {
		err = errors.Join(err, fmt.Errorf("terminal output: %w", copyErr))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func terminalEOF(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}

// Attach transfers input directly to the session. Ctrl-] detaches. Passwords
// are neither parsed nor stored; the child's terminal controls password echo.
func (m *TerminalManager) Attach(ctx context.Context, s *TerminalSession, input io.Reader, output io.Writer) error {
	m.attachMu.Lock()
	defer m.attachMu.Unlock()
	f, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return errors.New("interactive input requires a terminal")
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	state, err := term.MakeRaw(int(f.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(f.Fd()), state) //nolint:errcheck
	reader, err := cancelreader.NewReader(f)
	if err != nil {
		return err
	}
	defer reader.Close()
	// Bubble Tea is suspended during handoff, so relay resizes here too.
	resize := make(chan os.Signal, 1)
	stopResize := make(chan struct{})
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	defer close(stopResize)
	_ = pty.InheritSize(f, s.file)
	go func() {
		for {
			select {
			case <-resize:
				_ = pty.InheritSize(f, s.file)
			case <-stopResize:
				return
			}
		}
	}()
	s.mu.Lock()
	_, err = fmt.Fprint(output, "\r\nInteractive terminal · Ctrl-] returns to up\r\n\r\n", s.tail)
	if err == nil {
		s.attached = output
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		s.mu.Lock()
		s.attached = nil
		s.dismissed = true
		s.mu.Unlock()
	}()
	inputDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				data := buf[:n]
				detach := bytes.IndexByte(data, 0x1d)
				if detach >= 0 {
					data = data[:detach]
				}
				if len(data) > 0 {
					s.mu.Lock()
					s.prompt, s.dismissed = false, false
					s.mu.Unlock()
					_, err = s.file.Write(data)
				}
				if detach >= 0 {
					inputDone <- nil
					return
				}
			}
			if err != nil {
				inputDone <- err
				return
			}
		}
	}()
	select {
	case err := <-inputDone:
		return err
	case <-s.done:
	case <-ctx.Done():
	}
	reader.Cancel()
	<-inputDone
	return nil
}
