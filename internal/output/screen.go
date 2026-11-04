// Package output interprets bounded subprocess output for display. It never
// affects command execution, prompts, or the raw log written by the engine.
package output

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const maxRows = 256
const maxColumns = 512

// Screen interprets the line redraw operations used by CLI progress displays.
// It is a bounded text projection, not a terminal: colors, OSC, hyperlinks and
// terminal queries are discarded. Feed preserves parser state across writes.
type Screen struct {
	parser    *ansi.Parser
	rows      [][]rune
	x, y      int
	width     int
	continued bool // an implicit wrap split a token; suppress its fragments
	dirty     map[int]bool
	onLine    func(string)
}

func NewScreen(onLine func(string)) *Screen {
	s := &Screen{rows: [][]rune{nil}, dirty: make(map[int]bool), onLine: onLine, width: maxColumns}
	s.parser = ansi.NewParser()
	s.parser.SetDataSize(1024)
	s.parser.SetHandler(ansi.Handler{Print: s.print, Execute: s.control, HandleCsi: s.csi})
	return s
}

func (s *Screen) Feed(p []byte) {
	for _, b := range p {
		s.parser.Advance(b)
	}
}

func (s *Screen) Flush() { s.publish() }

// SetWidth matches the private PTY's columns, including implicit line wraps.
// Zero keeps plain/non-terminal output bounded without imposing a narrow wrap.
func (s *Screen) SetWidth(width int) {
	if width <= 0 {
		width = maxColumns
	}
	width = min(maxColumns, width)
	if s.width != width {
		// Reflowing arbitrary historical cursor output is ambiguous. Start a
		// fresh projection; raw logs and already-observed items remain intact.
		s.rows = [][]rune{nil}
		s.x, s.y = 0, 0
		s.continued = false
		clear(s.dirty)
	}
	s.width = width
}

func (s *Screen) Text() string {
	lines := make([]string, len(s.rows))
	for i, row := range s.rows {
		lines[i] = rowText(row)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (s *Screen) publish() {
	// Ascending order matters when an old rustup component announcement is
	// followed by a progress line without its own component name.
	for i, row := range s.rows {
		if !s.continued && s.dirty[i] && s.onLine != nil {
			s.onLine(strings.TrimSpace(rowText(row)))
		}
	}
	clear(s.dirty)
}

func rowText(row []rune) string {
	var b strings.Builder
	for _, r := range row {
		if r != 0 {
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func (s *Screen) ensure() {
	for len(s.rows) <= s.y {
		s.rows = append(s.rows, nil)
	}
}

func (s *Screen) print(r rune) {
	if unicode.IsControl(r) {
		return
	}
	w := ansi.StringWidth(string(r))
	if w == 0 {
		// This projection uses cells; combining marks are omitted.
		return
	}
	if w > s.width {
		return
	}
	if s.x+w > s.width {
		row := s.rows[s.y]
		// CLI tables use space-padded physical rows; an ordinary long line
		// can instead wrap inside an identity. Never parse those fragments.
		if len(row) == 0 || !unicode.IsSpace(row[len(row)-1]) {
			s.continued = true
		}
		s.publish()
		s.nextRow()
	}
	s.ensure()
	row := s.rows[s.y]
	for len(row) < min(maxColumns, s.x+w) {
		row = append(row, ' ')
	}
	// Clear a wide glyph when overwriting its trailing cell.
	if s.x > 0 && row[s.x] == 0 {
		row[s.x-1] = ' '
	}
	if ansi.StringWidth(string(row[s.x])) == 2 && s.x+1 < len(row) {
		row[s.x+1] = ' '
	}
	row[s.x] = r
	if w == 2 && s.x+1 < len(row) {
		row[s.x+1] = 0
	}
	s.rows[s.y] = row
	s.dirty[s.y] = true
	s.x = min(s.width, s.x+w)
}

func (s *Screen) control(b byte) {
	switch b {
	case '\r':
		s.publish()
		s.continued = false
		s.x = 0
	case '\n', '\v', '\f':
		s.publish()
		s.continued = false
		s.nextRow()
	case '\b':
		s.x = max(0, s.x-1)
	case '\t':
		end := min(s.width, (s.x/8+1)*8)
		for s.x < end {
			s.print(' ')
		}
	}
}

func (s *Screen) nextRow() {
	s.x = 0
	if s.y == maxRows-1 {
		copy(s.rows, s.rows[1:])
		s.rows[len(s.rows)-1] = nil
	} else {
		s.y++
		s.ensure()
	}
}

func (s *Screen) csi(cmd ansi.Cmd, params ansi.Params) {
	if cmd.Prefix() != 0 {
		return
	}
	value := func(i, def int) int { v, _, _ := params.Param(i, def); return v }
	n := min(maxColumns, max(1, value(0, 1)))
	switch cmd.Final() {
	case 'A', 'F':
		s.publish()
		s.continued = false
		s.y = max(0, s.y-n)
		if cmd.Final() == 'F' {
			s.x = 0
		}
	case 'B', 'E':
		s.publish()
		s.continued = false
		s.y = min(maxRows-1, s.y+n)
		s.ensure()
		if cmd.Final() == 'E' {
			s.x = 0
		}
	case 'C':
		s.x = min(s.width-1, s.x+n)
	case 'D':
		s.x = max(0, s.x-n)
	case 'G', '`':
		s.publish()
		s.continued = false
		s.x = min(s.width-1, max(0, value(0, 1)-1))
	case 'H', 'f':
		s.publish()
		s.continued = false
		s.y = min(maxRows-1, max(0, value(0, 1)-1))
		s.x = min(s.width-1, max(0, value(1, 1)-1))
		s.ensure()
	case 'K':
		s.ensure()
		row := s.rows[s.y]
		switch value(0, 0) {
		case 0:
			if s.x < len(row) {
				row = row[:s.x]
			}
		case 1:
			for i := 0; i < min(len(row), s.x+1); i++ {
				row[i] = ' '
			}
		case 2:
			row = nil
		}
		s.rows[s.y] = row
		s.publish()
	case 'J':
		s.publish()
		if value(0, 0) == 2 || value(0, 0) == 3 {
			s.rows = make([][]rune, s.y+1)
		}
	}
}
