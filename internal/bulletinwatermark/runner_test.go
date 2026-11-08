package bulletinwatermark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWatermarkProcess is a credential-free child process that exercises the
// real supervisor without loading a model in a unit test.
func TestWatermarkProcess(t *testing.T) {
	dir := ""
	for i, arg := range os.Args {
		if arg == "--job-dir" && i+1 < len(os.Args) {
			dir = os.Args[i+1]
		}
	}
	if dir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "input.pdf"))
	if err != nil {
		os.Exit(2)
	}
	if string(data) == "scoped" {
		var request struct {
			Scope map[string]any `json:"scope"`
		}
		manifest, _ := os.ReadFile(filepath.Join(dir, "request.json"))
		if json.Unmarshal(manifest, &request) != nil || len(request.Scope) != 7 || request.Scope["version"] != float64(1) || request.Scope["issueId"] != "00000000-0000-4000-8000-000000000001" || request.Scope["series"] != "general" || request.Scope["accountId"] != nil {
			os.Exit(2)
		}
	}
	output := append([]byte("%PDF-marked"), data...)
	if os.WriteFile(filepath.Join(dir, "output.pdf"), output, 0600) != nil {
		os.Exit(2)
	}
	hash := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	metadata := RenderMetadata{Status: "encoded_pdf", SourceSHA256: hash(data), OutputSHA256: hash(output), EngineVersion: "test", RendererVersion: "test-pdf-invisible", ModelSHA256: hash(nil), Pages: []PageMetadata{{Page: 1, Width: 1080, Height: 1528, SourceSHA256: hash(data), OutputSHA256: hash(output)}}}
	if string(data) == "tamper" {
		metadata.OutputSHA256 = hash(nil)
	}
	if os.Getenv("DAPR_API_TOKEN") != "" {
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(metadata)
	os.Exit(0)
}

func TestRunnerValidatesSubprocessAndCleansOutput(t *testing.T) {
	binary, _ := os.Executable()
	script := filepath.Join(t.TempDir(), "helper")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec "+quote(binary)+" -test.run=TestWatermarkProcess -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(script, filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	t.Setenv("DAPR_API_TOKEN", "synthetic-must-not-inherit")
	payload, _ := NewFingerprint()
	for _, source := range []string{"source", "scoped", "tamper"} {
		var out *Output
		if source == "scoped" {
			out, err = runner.RenderScoped(context.Background(), strings.NewReader(source), payload, PDFScopeMetadata{Version: 1, IssueID: "00000000-0000-4000-8000-000000000001", Series: "general", Locale: "zh-Hant", Revision: 1, SourceSHA256: strings.Repeat("a", 64), RendererVersion: "test-pdf-invisible"}, nil)
		} else {
			out, err = runner.Render(context.Background(), strings.NewReader(source), payload)
		}
		if source == "tamper" {
			if err == nil {
				out.Close()
				t.Fatal("unverified output accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		name := out.Name()
		if out.Metadata.RendererVersion != "test-pdf-invisible" {
			t.Fatal("missing reconstruction metadata")
		}
		out.Close()
		if _, err = os.Stat(name); !os.IsNotExist(err) {
			t.Fatal("private output retained")
		}
	}
	code, _ := NewCode()
	if _, err = runner.Render(context.Background(), strings.NewReader("source"), code); err == nil {
		t.Fatal("legacy text code accepted")
	}
}

func TestRunnerRejectsUnavailableHelper(t *testing.T) {
	if _, err := NewRunner("/no-such-hhc-renderer", t.TempDir()); err == nil {
		t.Fatal("missing helper accepted")
	}
}
func TestRunnerRejectsOversizeAndCleans(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "private")
	runner, err := NewRunner(executable, root)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := NewFingerprint()
	if _, err = runner.Render(context.Background(), strings.NewReader(strings.Repeat("x", 20<<20+1)), code); err == nil {
		t.Fatal("oversize input accepted")
	}
	defer runner.Close()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".lock" {
		t.Fatalf("leaked tempfiles: %v %d", err, len(entries))
	}
}

func TestRunnerRemovesFreshCrashFilesAndProtectsLiveRunner(t *testing.T) {
	executable, _ := os.Executable()
	root := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(filepath.Join(root, "render-crashed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "render-crashed", "input.pdf"), []byte("private document"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := NewRunner(executable, root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err = os.Stat(filepath.Join(root, "render-crashed")); !os.IsNotExist(err) {
		t.Fatal("fresh crash document survived")
	}
	if _, err = NewRunner(executable, root); err == nil {
		t.Fatal("second runner could sweep live files")
	}
}

func TestRunnerCleansCrashFilesWhenHelperMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(filepath.Join(root, "render-crashed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "render-crashed", "output.pdf"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner("/missing-watermark-helper", root); err == nil {
		t.Fatal("missing helper accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "render-crashed")); !os.IsNotExist(err) {
		t.Fatal("private crash files not removed")
	}
}
