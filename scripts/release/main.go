// Command release builds versioned archives locally. It never uploads or
// publishes artifacts and refuses to overwrite an existing output directory.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

var targets = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"}
var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func main() {
	version := flag.String("version", "", "release version, e.g. v0.1.0 or v0.1.0-rc.1")
	output := flag.String("output", "dist", "new directory for release archives")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "release: unexpected positional arguments")
		os.Exit(2)
	}
	if err := build(*version, *output); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func validVersion(v string) bool {
	if !versionPattern.MatchString(v) {
		return false
	}
	base, _, _ := strings.Cut(v, "+")
	_, pre, _ := strings.Cut(base, "-")
	for _, part := range strings.Split(pre, ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return false
		}
	}
	return true
}

func build(version, output string) error {
	if !validVersion(version) {
		return errors.New("--version must be a version such as v0.1.0 or v0.1.0-rc.1")
	}
	if _, err := os.Stat("go.mod"); err != nil {
		return errors.New("run from the repository root")
	}
	output, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("output directory must not exist: %s", output)
	}
	stage, err := os.MkdirTemp(filepath.Dir(output), ".up-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	licenses, err := dependencyLicenses()
	if err != nil {
		return err
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		return err
	}
	license, err := os.ReadFile("LICENSE")
	if err != nil {
		return err
	}
	var checksums strings.Builder
	for _, target := range targets {
		goos, arch, _ := strings.Cut(target, "/")
		name := fmt.Sprintf("up_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), goos, arch)
		binary := filepath.Join(stage, "up")
		cmd := exec.Command("go", "build", "-mod=readonly", "-trimpath", "-buildvcs=true", "-ldflags=-s -w -X main.version="+version, "-o", binary, "./cmd/up")
		cmd.Env = targetEnv(goos, arch)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", target, err)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		files := []archiveFile{{"up", 0o755, data}, {"README.md", 0o644, readme}, {"LICENSE", 0o644, license}, {"THIRD_PARTY_LICENSES.txt", 0o644, licenses}}
		path := filepath.Join(stage, name)
		if err := writeArchive(path, files); err != nil {
			return err
		}
		packed, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(packed), name)
		fmt.Println(name)
	}
	if err := os.Remove(filepath.Join(stage, "up")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "checksums.txt"), []byte(checksums.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(stage, output)
}

func targetEnv(goos, arch string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOAMD64", "GOARM64":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0", "GOAMD64=v1", "GOARM64=v8.0")
}

type module struct {
	Path, Version, Dir string
	Main               bool
}
type dependency struct{ Module *module }

// Collect notices for the union of runtime dependencies across all targets.
// Missing license files stop packaging rather than silently dropping notices.
func dependencyLicenses() ([]byte, error) {
	modules := map[string]module{}
	for _, target := range targets {
		goos, arch, _ := strings.Cut(target, "/")
		cmd := exec.Command("go", "list", "-mod=readonly", "-deps", "-json", "./cmd/up")
		cmd.Env = targetEnv(goos, arch)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		data, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("list %s dependencies: %w: %s", target, err, stderr.String())
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var p dependency
			if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, err
			}
			if p.Module != nil && !p.Module.Main {
				modules[p.Module.Path] = *p.Module
			}
		}
	}
	var paths []string
	for path := range modules {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var notices bytes.Buffer
	goLicense, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&notices, "Go standard library (%s)\n\n%s\n", runtime.Version(), goLicense)
	for _, path := range paths {
		m := modules[path]
		entries, err := os.ReadDir(m.Dir)
		if err != nil {
			return nil, err
		}
		found := false
		for _, entry := range entries {
			name := strings.ToUpper(entry.Name())
			if entry.IsDir() || !(strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "COPYING") || strings.HasPrefix(name, "NOTICE")) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(m.Dir, entry.Name()))
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&notices, "\n============================================================\n%s %s — %s\n\n%s\n", path, m.Version, entry.Name(), data)
			found = true
		}
		if !found {
			return nil, fmt.Errorf("no license notice found for %s %s", path, m.Version)
		}
	}
	return notices.Bytes(), nil
}

type archiveFile struct {
	name string
	mode int64
	data []byte
}

func writeArchive(path string, files []archiveFile) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	var writeErr error
	for _, file := range files {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			writeErr = err
			break
		}
		if _, err := tw.Write(file.data); err != nil {
			writeErr = err
			break
		}
	}
	return errors.Join(writeErr, tw.Close(), zw.Close(), f.Close())
}
