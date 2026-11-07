package report

import (
	"fmt"
	"regexp"
	"strings"
)

var npmResource = regexp.MustCompile(`requested resource '([^'\r\n]+)'`)
var npmName = regexp.MustCompile(`^(?:@[A-Za-z0-9._-]+/)?[A-Za-z0-9._-]+$`)

func npmLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"npm error ", "npm ERR! "} {
		if text, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(text), true
		}
	}
	return "", false
}

func npmCode(lines []string) string {
	for _, line := range lines {
		if text, ok := npmLine(line); ok && strings.HasPrefix(text, "code ") {
			code := strings.TrimSpace(strings.TrimPrefix(text, "code "))
			if len(code) < 2 || len(code) > 32 || code[0] != 'E' {
				continue
			}
			valid := true
			for _, r := range code {
				if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
					valid = false
				}
			}
			if valid {
				return code
			}
		}
	}
	return ""
}

func npmFailure(lines []string) string {
	if npmMissingCommand(lines) {
		return "npm: an install script could not find npm (exit 127)."
	}
	code := npmCode(lines)
	switch code {
	case "E404":
		for _, line := range lines {
			text, ok := npmLine(line)
			if !ok {
				continue
			}
			if m := npmResource.FindStringSubmatch(text); m != nil {
				name := m[1]
				if at := strings.LastIndex(name, "@"); at > 0 {
					name = name[:at]
				}
				if len(name) <= 214 && npmName.MatchString(name) {
					return fmt.Sprintf("npm: registry lookup failed for %s (E404).", name)
				}
			}
		}
		return "npm: package not found or registry access denied (E404)."
	case "E401", "E403":
		return fmt.Sprintf("npm: registry authentication or access failed (%s).", code)
	case "EACCES", "EPERM":
		return fmt.Sprintf("npm: permission denied (%s).", code)
	case "":
		return ""
	default:
		return fmt.Sprintf("npm failed (%s). Inspect the log for details.", code)
	}
}

func npmMissingCommand(lines []string) bool {
	for _, line := range lines {
		if text, ok := npmLine(line); ok && (strings.Contains(text, "npm: command not found") || strings.Contains(text, "npm: not found")) {
			return true
		}
	}
	return false
}
