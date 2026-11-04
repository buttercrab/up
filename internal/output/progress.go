package output

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Item is the last observation for one package/component. Downloaded is not
// installed. Total == 0 means the upstream tool supplied no byte total.
type Item struct {
	Name, Version, Phase, Detail string
	Current, Total               float64 // bytes
}

func (i Item) Fraction() (float64, bool) {
	if i.Total <= 0 {
		return 0, false
	}
	return min(1, max(0, i.Current/i.Total)), true
}

// Tracker recognizes a deliberately small set of human-readable formats.
// Unknown/localized output remains available in Screen/Text and raw logs.
// Storage is capped independently of command output and snapshots are owned.
type Tracker struct {
	tool                 string
	items                []Item
	component, toolchain string
}

func NewTracker(tool string) *Tracker { return &Tracker{tool: tool} }
func (t *Tracker) Items() []Item      { return slices.Clone(t.items) }

var (
	brewDownload  = regexp.MustCompile(`(?:^|\s)(Bottle|Cask) ([[:alnum:]@+_.-]+)(?: \(([^)]+)\))?`)
	bytesPair     = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*([KMGT]?i?B)\s*/\s*([0-9]+(?:\.[0-9]+)?)\s*([KMGT]?i?B)`)
	bytesSingle   = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*([KMGT]?i?B)(?:\s|/|$)`)
	rustRow       = regexp.MustCompile(`^([[:alnum:]_.-]+)\s+(downloading|downloaded|installing|installed|extracting|unpacking|pending installation)\b(.*)$`)
	rustComponent = regexp.MustCompile(`(?:downloading|installing) component ['"]?([[:alnum:]_.-]+)['"]?(?: for ['"]?([[:alnum:]_.-]+))?`)
	rustChannel   = regexp.MustCompile(`syncing channel updates for ['"]?([[:alnum:]_.-]+)`)
	npmRun        = regexp.MustCompile(`^npm (?:info|verbose) run (\S+)@([^@\s]+)\s`)
)

func bytesValue(number, unit string) float64 {
	n, _ := strconv.ParseFloat(number, 64)
	power := strings.Index("BKMGT", string(unit[0]))
	if power < 0 {
		return 0
	}
	base := 1000.0
	if strings.Contains(unit, "i") {
		base = 1024
	}
	return n * math.Pow(base, float64(power))
}

func amounts(item *Item, text string) {
	if m := bytesPair.FindStringSubmatch(text); m != nil {
		item.Current, item.Total = bytesValue(m[1], m[2]), bytesValue(m[3], m[4])
		item.Detail = m[0]
	} else if m := bytesSingle.FindStringSubmatch(text); m != nil {
		item.Current = bytesValue(m[1], m[2])
		// Keep the byte counter and rate/ETA, excluding any unrelated text.
		item.Detail = m[1] + " " + m[2]
		tail := text[strings.Index(text, m[0])+len(m[0]):]
		if start := strings.Index(tail, "("); start >= 0 {
			if end := strings.Index(tail[start:], ")"); end >= 0 {
				item.Detail += " " + tail[start:start+end+1]
			}
		}
	}
}

func (t *Tracker) Observe(line string) {
	switch t.tool {
	case "brew":
		t.brew(line)
	case "rustup":
		t.rustup(line)
	case "npm":
		if m := npmRun.FindStringSubmatch(line); m != nil {
			phase := "running script"
			if strings.Contains(line, "code: 0") {
				phase = "script finished"
			}
			t.put(Item{Name: m[1], Version: m[2], Phase: phase})
		}
	}
}

func (t *Tracker) put(item Item) {
	for i, old := range t.items {
		if old.Name == item.Name {
			if item.Version == "" {
				item.Version = old.Version
			}
			// Preserve bytes for an abbreviated download-completed line only.
			if item.Phase == "downloaded" && item.Detail == "" {
				item.Current, item.Total, item.Detail = old.Current, old.Total, old.Detail
			}
			t.items[i] = item
			return
		}
	}
	if len(t.items) == 128 {
		t.items = slices.Delete(t.items, 0, 1)
	}
	t.items = append(t.items, item)
}

func (t *Tracker) brew(line string) {
	if strings.Contains(line, "Bottle Manifest ") || strings.Contains(line, "JSON API ") {
		return
	}
	if m := brewDownload.FindStringSubmatch(line); m != nil {
		item := Item{Name: m[2], Version: m[3]}
		switch {
		case strings.Contains(line, "Downloading"):
			item.Phase = "downloading"
		case strings.Contains(line, "Downloaded"), strings.Contains(line, "✔"):
			item.Phase = "downloaded"
		default:
			return
		}
		amounts(&item, line)
		t.put(item)
		return
	}
	for _, p := range []struct{ prefix, phase string }{
		{"==> Upgrading ", "upgrading"}, {"==> Installing ", "installing"},
		{"==> Pouring ", "installing"}, {"==> Purging files for version ", "cleaning up"},
	} {
		if !strings.HasPrefix(line, p.prefix) {
			continue
		}
		name := strings.TrimPrefix(line, p.prefix)
		if p.phase == "cleaning up" {
			_, name, _ = strings.Cut(name, " of Cask ")
		}
		if p.prefix == "==> Pouring " {
			name, _, _ = strings.Cut(name, "--")
		}
		fields := strings.Fields(name)
		if len(fields) == 0 || strings.ContainsAny(fields[0], "/:()") {
			return
		}
		if fields[0] == "Cask" && len(fields) > 1 {
			fields = fields[1:]
		}
		if fields[0] == "dependencies" || fields[0] == "dependency" {
			return
		}
		// Numeric aggregate summaries are not package names.
		if _, err := strconv.Atoi(fields[0]); err == nil {
			return
		}
		t.put(Item{Name: fields[0], Phase: p.phase})
		return
	}
}

func (t *Tracker) rustup(line string) {
	if m := rustChannel.FindStringSubmatch(line); m != nil {
		t.toolchain = m[1]
		return
	}
	qualify := func(name string) string {
		if t.toolchain != "" {
			return t.toolchain + " / " + name
		}
		return name
	}
	if m := rustRow.FindStringSubmatch(line); m != nil {
		item := Item{Name: qualify(m[1]), Phase: m[2]}
		amounts(&item, m[3])
		t.put(item)
		return
	}
	if m := rustComponent.FindStringSubmatch(line); m != nil {
		t.component = m[1]
		if m[2] != "" {
			t.component += "-" + m[2]
		}
		phase := "downloading"
		if strings.Contains(line, "installing component") {
			phase = "installing"
		}
		t.put(Item{Name: qualify(t.component), Phase: phase})
		return
	}
	// Older rustup versions report one anonymous byte counter at a time.
	if t.component != "" && bytesPair.MatchString(line) {
		item := Item{Name: qualify(t.component), Phase: "downloading"}
		amounts(&item, line)
		t.put(item)
	}
}
