package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/buttercrab/up/internal/app"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	info, _ := debug.ReadBuildInfo()
	code := app.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, resolvedVersion(version, info))
	stop()
	os.Exit(code)
}

// go install module@tag does not receive release linker flags. Preserve the
// module's version in that case, and identify checkout builds by VCS revision.
func resolvedVersion(linked string, info *debug.BuildInfo) string {
	if linked != "dev" || info == nil {
		return linked
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return linked
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	result := linked + "+" + revision
	if dirty {
		result += ".dirty"
	}
	return result
}
