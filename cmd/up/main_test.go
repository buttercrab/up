package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, linked string
		info         *debug.BuildInfo
		want         string
	}{
		{"release linker flag", "v0.1.0", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.1"}}, "v0.1.0"},
		{"tagged go install", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "v0.1.0"},
		{"checkout", "dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "123456789abcdef"}, {Key: "vcs.modified", Value: "true"}}}, "dev+123456789abc.dirty"},
		{"no build info", "dev", nil, "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvedVersion(tc.linked, tc.info); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
