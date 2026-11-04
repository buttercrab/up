package process

import (
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// TerminalManager executes commands in private terminals, allowing a UI to
// attach to the same process for prompts without retrying an upgrade.
// Only one session can own user input at a time.
type TerminalManager struct {
	mu         sync.Mutex
	attachMu   sync.Mutex
	sessions   map[string]*TerminalSession
	cols, rows int
}

type TerminalSession struct {
	mu        sync.Mutex
	file      *os.File
	done      chan struct{}
	output    io.Writer
	attached  io.Writer
	tail      string
	prompt    bool
	dismissed bool
}

func (m *TerminalManager) Session(id string) *TerminalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (s *TerminalSession) NeedsInput() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prompt && !s.dismissed
}

func (s *TerminalSession) DismissPrompt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dismissed = true
}

func (s *TerminalSession) Write(p []byte) (int, error) {
	if _, err := s.output.Write(p); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail += string(p)
	if len(s.tail) > 8192 {
		s.tail = s.tail[len(s.tail)-8192:]
	}
	// Detection is a convenience, not a requirement for input: users can attach
	// manually for localized or otherwise unrecognized prompts.
	text := strings.TrimRight(strings.ToLower(ansi.Strip(s.tail)), "\r\n \t")
	line := text[strings.LastIndexAny(text, "\r\n")+1:]
	for _, prompt := range []string{"password:", "password for ", "passphrase", "[y/n]", "[y/n/", "(y/n)", "[yes/no]"} {
		if strings.Contains(line, prompt) {
			s.prompt = true
		}
	}
	if s.attached != nil {
		if _, err := s.attached.Write(p); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}
