//go:build mage

// Package main hosts the mage build/lint/test targets for routeros-mcp.
// Run "mage -l" to list targets.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aexvir/harness"
	"github.com/aexvir/harness/commons"
	"github.com/magefile/mage/mg" //nolint:depguard // build tool dep only used in magefile

	"github.com/czechbol/routeros-mcp/internal/sharder"
)

const (
	module          = "github.com/czechbol/routeros-mcp"
	binaryName      = "routeros-mcp"
	distDir         = "dist"
	imageName       = "routeros-mcp"
	golangciVer     = "2.12.2"
	defaultRegistry = "ghcr.io"
	dirPerm         = 0o755
)

var platforms = []platform{
	{goos: "linux", goarch: "arm64"},
	{goos: "linux", goarch: "arm", goarm: "7"},
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "mips"},
	{goos: "linux", goarch: "mipsle"},
}

type platform struct {
	goos, goarch, goarm string
}

func (p platform) tag() string {
	if p.goarm != "" {
		return fmt.Sprintf("%s-%sv%s", p.goos, p.goarch, p.goarm)
	}
	return fmt.Sprintf("%s-%s", p.goos, p.goarch)
}

func run(ctx context.Context, tasks ...harness.Task) error {
	return harness.New().Execute(ctx, tasks...)
}

// Tidy runs go mod tidy.
func Tidy(ctx context.Context) error { return run(ctx, commons.GoModTidy()) }

// Format runs gofmt and goimports on the entire tree.
func Format(ctx context.Context) error {
	return run(ctx, commons.GoFmt(), commons.GoImports(module))
}

// Lint installs (if needed) and runs golangci-lint.
func Lint(ctx context.Context) error {
	return run(ctx, commons.GolangCILint(commons.WithGolangCIVersion(golangciVer)))
}

// Test runs the full test suite.
func Test(ctx context.Context) error { return run(ctx, commons.GoTest()) }

// Build compiles the host-arch binary into dist/.
func Build(ctx context.Context) error {
	if err := os.MkdirAll(distDir, dirPerm); err != nil {
		return fmt.Errorf("mkdir dist: %w", err)
	}
	return run(ctx, buildTask(platform{goos: runtimeGOOS(), goarch: runtimeGOARCH()}))
}

// BuildAll cross-compiles for every supported architecture.
func BuildAll(ctx context.Context) error {
	if err := os.MkdirAll(distDir, dirPerm); err != nil {
		return fmt.Errorf("mkdir dist: %w", err)
	}
	tasks := make([]harness.Task, 0, len(platforms))
	for _, p := range platforms {
		tasks = append(tasks, buildTask(p))
	}
	return run(ctx, tasks...)
}

func buildTask(p platform) harness.Task {
	return func(ctx context.Context) error {
		var out string
		if p.goos == runtimeGOOS() && p.goarch == runtimeGOARCH() && p.goarm == "" {
			out = filepath.Join(distDir, binaryName)
		} else {
			out = filepath.Join(distDir, fmt.Sprintf("%s-%s", binaryName, p.tag()))
		}
		harness.LogStep(fmt.Sprintf("build %s -> %s", p.tag(), out))
		envs := []string{
			"CGO_ENABLED=0",
			"GOOS=" + p.goos,
			"GOARCH=" + p.goarch,
		}
		if p.goarm != "" {
			envs = append(envs, "GOARM="+p.goarm)
		}
		cmd := exec.CommandContext(
			ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, ".",
		)
		cmd.Env = append(os.Environ(), envs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go build %s: %w", p.tag(), err)
		}
		return nil
	}
}

func runtimeGOOS() string {
	if v := os.Getenv("GOOS"); v != "" {
		return v
	}
	return goEnv("GOOS")
}

func runtimeGOARCH() string {
	if v := os.Getenv("GOARCH"); v != "" {
		return v
	}
	return goEnv("GOARCH")
}

func goEnv(key string) string {
	cmd := exec.Command("go", "env", key)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var dockerPlatforms = []string{
	"linux/arm64",
	"linux/arm/v7",
	"linux/amd64",
}

// Tarballs builds per-arch OCI images via buildx and saves them under dist/
// as files RouterOS can import via /container/add. Does not push anywhere.
func Tarballs(ctx context.Context) error {
	if err := os.MkdirAll(distDir, dirPerm); err != nil {
		return fmt.Errorf("mkdir dist: %w", err)
	}
	version := tarballVersion()
	tasks := make([]harness.Task, 0, len(dockerPlatforms))
	for _, plat := range dockerPlatforms {
		safe := strings.ReplaceAll(plat, "/", "-")
		tag := fmt.Sprintf("%s:%s", imageName, safe)
		out := filepath.Join(distDir, fmt.Sprintf("%s-%s-%s.tar", imageName, version, safe))
		tasks = append(tasks, func(ctx context.Context) error {
			harness.LogStep(fmt.Sprintf("docker build %s -> load", plat))
			build := exec.CommandContext(ctx, "docker", "buildx", "build",
				"--platform", plat,
				"-t", tag,
				"--load",
				"-f", "Dockerfile",
				".",
			)
			build.Stdout = os.Stdout
			build.Stderr = os.Stderr
			if err := build.Run(); err != nil {
				return fmt.Errorf("buildx %s: %w", plat, err)
			}
			harness.LogStep(fmt.Sprintf("docker save %s -> %s", tag, out))
			save := exec.CommandContext(ctx, "docker", "save", tag, "-o", out)
			save.Stdout = os.Stdout
			save.Stderr = os.Stderr
			if err := save.Run(); err != nil {
				return fmt.Errorf("docker save %s: %w", plat, err)
			}
			return nil
		})
	}
	return run(ctx, tasks...)
}

// Release pushes a multi-arch image to REGISTRY/IMAGE_REPO and produces
// per-arch tarballs under dist/. Designed to run from CI after docker login.
//
// Env vars:
//   - REGISTRY     (default ghcr.io)
//   - IMAGE_REPO   (default $GITHUB_REPOSITORY, required if unset)
//   - VERSION      (default $GITHUB_REF_NAME, required if unset)
//   - PUSH         (default "1"; set "0" to skip the registry push)
func Release(ctx context.Context) error {
	version, err := releaseVersion()
	if err != nil {
		return err
	}
	repo, err := releaseRepo()
	if err != nil {
		return err
	}
	registry := getenv("REGISTRY", defaultRegistry)
	base := fmt.Sprintf("%s/%s", registry, strings.ToLower(repo))
	tags := semverTags(version)

	if os.Getenv("PUSH") != "0" {
		if err := pushMultiArch(ctx, base, tags); err != nil {
			return err
		}
	}
	mg.CtxDeps(ctx, Tarballs)
	return nil
}

func pushMultiArch(ctx context.Context, base string, tags []string) error {
	args := []string{
		"buildx", "build",
		"--platform", strings.Join(dockerPlatforms, ","),
		"--push",
		"--provenance=mode=max",
		"--sbom=true",
		"-f", "Dockerfile",
	}
	for _, t := range tags {
		args = append(args, "-t", base+":"+t)
	}
	args = append(args, ".")
	harness.LogStep(fmt.Sprintf("docker buildx push %s -> %v", base, tags))
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buildx push: %w", err)
	}
	return nil
}

func releaseVersion() (string, error) {
	v := os.Getenv("VERSION")
	if v == "" {
		v = os.Getenv("GITHUB_REF_NAME")
	}
	if v == "" {
		return "", fmt.Errorf("VERSION (or GITHUB_REF_NAME) required for release")
	}
	return strings.TrimPrefix(v, "v"), nil
}

func releaseRepo() (string, error) {
	repo := os.Getenv("IMAGE_REPO")
	if repo == "" {
		repo = os.Getenv("GITHUB_REPOSITORY")
	}
	if repo == "" {
		return "", fmt.Errorf("IMAGE_REPO (or GITHUB_REPOSITORY) required for release")
	}
	return repo, nil
}

func tarballVersion() string {
	if v, err := releaseVersion(); err == nil {
		return v
	}
	return "dev"
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// semverTags expands a semver into the tag aliases we publish. Pre-release
// versions (anything containing "-" or "+") get only the exact tag.
func semverTags(version string) []string {
	tags := []string{version}
	if strings.ContainsAny(version, "-+") {
		return tags
	}
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		tags = append(tags, parts[0]+"."+parts[1])
	}
	if len(parts) >= 1 {
		tags = append(tags, parts[0])
	}
	tags = append(tags, "latest")
	return tags
}

// Shards regenerates the per-menu OpenAPI shards under tools/openapi/ from
// the local mikrotik-openapi.json. Run whenever the upstream spec changes.
func Shards(_ context.Context) error {
	src := "mikrotik-openapi.json"
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf(
			"%s not found in cwd; download from https://tikoci.github.io/restraml/<version>/openapi.json", src,
		)
	}
	out := filepath.Join("tools", "openapi")
	idx, err := sharder.ShardFile(src, out)
	if err != nil {
		return err
	}
	harness.LogStep(fmt.Sprintf("sharded %d menus from RouterOS %s", len(idx.Menus), idx.SpecVersion))
	return nil
}

// Clean removes the dist directory.
func Clean() error { return os.RemoveAll(distDir) }

// Check runs format + lint + test in sequence (CI gate).
func Check(ctx context.Context) error {
	mg.SerialCtxDeps(ctx, Format, Lint, Test)
	return nil
}
