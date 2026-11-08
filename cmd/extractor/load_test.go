package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

// Opt-in release gate: the real readiness handler/DB stays responsive while a
// separate nonroot, no-network, 1CPU/2GiB process parses both sanitized PDFs and
// composes native HTML. This is local isolation evidence, not Azure capacity QA.
func TestExtractorIsolationLoadGate(t *testing.T) {
	testExtractorVersions(t, false)
}

// Every selected version still composes a real document with its bundled assets.
func TestExtractorRuntimeSmoke(t *testing.T) {
	testExtractorVersions(t, true)
}

func testExtractorVersions(t *testing.T, smoke bool) {
	image := os.Getenv("HHC_EXTRACTOR_LOAD_IMAGE")
	if image == "" {
		t.Skip("requires an explicitly selected extractor image")
	}
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("test database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	identity, err := exec.CommandContext(ctx, "docker", "image", "inspect", image, "--format", "{{.Id}} {{.Config.User}}").Output()
	fields := strings.Fields(string(identity))
	if err != nil || len(fields) != 2 || fields[1] != "65532:65532" || !strings.HasPrefix(fields[0], "sha256:") {
		t.Fatal("nonroot immutable image required")
	}
	image = fields[0]
	if output, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--read-only", image, "-verify-assets").CombinedOutput(); err != nil {
		t.Fatalf("bundled asset preflight failed: %v: %s", err, output)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("HHC_EXTRACTOR_TEST_BINARY")
	if binary != "" {
		if err := verifyParserBinary(binary, os.Getenv("HHC_EXTRACTOR_TEST_BINARY_SHA256")); err != nil {
			t.Fatal(err)
		}
	} else {
		binary = filepath.Join(t.TempDir(), "parser.test")
		build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, "./internal/onlinebulletins")
		build.Dir = root
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
		if err = build.Run(); err != nil {
			t.Fatal("load fixture compilation failed")
		}
	}
	version, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--read-only", image, "-version").Output()
	if err != nil {
		t.Fatal("extractor image unavailable")
	}
	var pins map[string]string
	if json.Unmarshal(version, &pins) != nil || len(pins["rendererArtifactSha256"]) != 64 {
		t.Fatal("invalid image identity")
	}
	// Compile and verify the same immutable image once; each version still gets
	// its own sequential 1CPU/2GiB process and concurrent readiness probes.
	for _, rendererVersion := range []string{"v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9", "v10", "v11"} {
		t.Run(rendererVersion, func(t *testing.T) {
			testExtractorIsolationLoadGate(t, rendererVersion, image, root, binary, pins, dsn, smoke)
		})
	}
}

// Reuse only the caller's same-job parser smoke binary, never a cross-run cache.
func verifyParserBinary(path, expected string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	if !filepath.IsAbs(path) || len(data) == 0 || expected != hex.EncodeToString(hash[:]) {
		return fmt.Errorf("invalid prepared parser binary")
	}
	return nil
}

func TestPreparedParserBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parser.test")
	data := []byte("same-job fixture")
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	expected := hex.EncodeToString(hash[:])
	if err := verifyParserBinary(path, expected); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct{ path, digest string }{
		{path, ""}, {path, strings.Repeat("0", 64)}, {path + ".missing", expected}, {"parser.test", expected},
	} {
		if verifyParserBinary(candidate.path, candidate.digest) == nil {
			t.Fatal("invalid prepared binary accepted")
		}
	}
	if err := os.WriteFile(path, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if verifyParserBinary(path, expected) == nil {
		t.Fatal("mutated prepared binary accepted")
	}
}

func testExtractorIsolationLoadGate(t *testing.T, rendererVersion, image, root, binary string, pins map[string]string, dsn string, smoke bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	server := httptest.NewServer(httpapi.New(nil, db, nil).Routes())
	defer server.Close()
	client := http.Client{Timeout: time.Second}
	probe := func() {
		t.Helper()
		response, err := client.Get(server.URL + "/health/ready")
		if err != nil {
			t.Fatal("readiness did not respond within one second")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("API became unready during isolated extraction")
		}
	}
	probe()
	containerName := "hhc-extractor-load-" + platform.NewID()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", containerName).Run()
	}()
	testPattern := "TestParsePDFControlledFixtures|TestLayoutRunnerNativeImage|TestSimplifiedLayoutRunnerNativeImage"
	if smoke {
		testPattern = "TestSimplifiedLayoutRunnerNativeImage"
		if rendererVersion == "v1" {
			testPattern = "TestLayoutRunnerRuntimeSmoke"
		}
	}
	command := exec.CommandContext(ctx, "docker", "run", "--name", containerName, "--rm", "--network", "none", "--read-only", "--memory", "2g", "--memory-swap", "2g", "--cpus", "1",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=128m",
		"--mount", "type=bind,src="+binary+",dst=/verification/parser.test,readonly",
		"--mount", "type=bind,src="+filepath.Join(root, "internal/onlinebulletins/testdata")+",dst=/fixture/testdata,readonly",
		"-w", "/fixture", "-e", "HHC_REQUIRE_POPPLER=1", "-e", "HHC_TEST_LAYOUT_RUNNER_ROOT=/opt/bulletin-renderer",
		"-e", "HHC_TEST_LAYOUT_RENDERER_DIGEST="+rendererV1ArtifactSHA256,
		"-e", "HHC_TEST_LAYOUT_V3_RENDERER_DIGEST="+rendererV3ArtifactSHA256,
		"-e", "HHC_TEST_LAYOUT_V4_RENDERER_DIGEST="+rendererV4ArtifactSHA256,
		"-e", "HHC_TEST_LAYOUT_V5_RENDERER_DIGEST="+rendererV5ArtifactSHA256,
		"-e", "HHC_TEST_LAYOUT_V6_RENDERER_DIGEST="+rendererV6ArtifactSHA256,
		"-e", "HHC_TEST_LAYOUT_V7_RENDERER_DIGEST="+rendererV7ArtifactSHA256,
		"-e", "HHC_TEST_LAYOUT_V8_RENDERER_DIGEST="+pins["rendererV8ArtifactSha256"],
		"-e", "HHC_TEST_LAYOUT_V9_RENDERER_DIGEST="+rendererV9ArtifactSHA256,
		"-e", "HHC_TEST_LAYOUT_V10_RENDERER_DIGEST="+pins["rendererArtifactSha256"],
		"-e", "HHC_TEST_LAYOUT_V11_RENDERER_DIGEST="+pins["rendererV11ArtifactSha256"],
		"-e", "HHC_TEST_LAYOUT_V2_RENDERER_DIGEST="+pins["rendererV2ArtifactSha256"],
		"--entrypoint", "/bin/sh", image, "-c", `/verification/parser.test -test.run '^(`+testPattern+`)$/^(1739-sanitized.pdf|1740-sanitized.pdf|`+rendererVersion+`)$' -test.count=1 -test.v
status=$?
peak=/sys/fs/cgroup/memory.peak
if [ ! -r "$peak" ]; then peak=/sys/fs/cgroup/memory/memory.max_usage_in_bytes; fi
printf 'MEMORY_PEAK_BYTES=%s\n' "$(cat "$peak")"
exit "$status"`)
	finished := make(chan error, 1)
	var output []byte
	go func() { var err error; output, err = command.CombinedOutput(); finished <- err }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	probes := 0
	for {
		select {
		case err := <-finished:
			if err != nil {
				t.Fatalf("bounded extraction/layout process failed: %v\n%s", err, output)
			}
			expected := []string{}
			if !smoke {
				expected = append(expected, "TestParsePDFControlledFixtures/1739-sanitized.pdf", "TestParsePDFControlledFixtures/1740-sanitized.pdf")
			}
			if rendererVersion == "v1" || (!smoke && (rendererVersion == "v7" || rendererVersion == "v8" || rendererVersion == "v9" || rendererVersion == "v10" || rendererVersion == "v11")) {
				name := "TestLayoutRunnerNativeImage/"
				if smoke {
					name = "TestLayoutRunnerRuntimeSmoke/"
				}
				expected = append(expected, name+rendererVersion)
			}
			if rendererVersion != "v1" {
				expected = append(expected, "TestSimplifiedLayoutRunnerNativeImage/"+rendererVersion)
			}
			for _, name := range expected {
				if !strings.Contains(string(output), "PASS: "+name) {
					t.Fatal("load fixture did not execute")
				}
			}
			if probes < 2 {
				t.Fatal("no concurrent readiness samples")
			}
			var peak uint64
			for _, line := range strings.Split(string(output), "\n") {
				if value, ok := strings.CutPrefix(line, "MEMORY_PEAK_BYTES="); ok {
					peak, err = strconv.ParseUint(value, 10, 64)
					if err != nil {
						t.Fatal("invalid peak memory measurement")
					}
				}
			}
			if peak == 0 || peak > 2<<30 {
				t.Fatal("missing or excessive peak memory measurement")
			}
			probe()
			t.Logf("%d successful readiness samples; planned parser/native layout cases passed within 1CPU/2GiB without swap; peak %d bytes", probes, peak)
			return
		case <-ticker.C:
			probe()
			probes++
		case <-ctx.Done():
			t.Fatal("isolation load gate timed out")
		}
	}
}
