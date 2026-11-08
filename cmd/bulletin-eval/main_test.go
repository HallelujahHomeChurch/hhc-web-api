package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func testPages(t *testing.T) []onlinebulletins.RecognitionPage {
	t.Helper()
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return []onlinebulletins.RecognitionPage{{SourceSHA256: strings.Repeat("a", 64), Page: 1, TotalPages: 1, Width: 612, Height: 792, PNG: pixels.Bytes(), Native: []onlinebulletins.RecognitionText{{ID: "p1-t1", Text: "PRIVATE_SOURCE_SENTINEL", Box: onlinebulletins.Box{X: .2, Y: .3, Width: .5, Height: .02}}}}}
}

func writeBundle(t *testing.T, path string, pages []onlinebulletins.RecognitionPage) {
	t.Helper()
	data, err := json.Marshal(evaluationBundle{Version: 1, Pages: pages})
	if err != nil || os.WriteFile(path, data, 0600) != nil {
		t.Fatal("write fixture")
	}
}

func TestEvaluationWritesPrivateResultsAndContentFreeUsage(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "source.json")
	writeBundle(t, bundle, testPages(t))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, _ := json.Marshal(onlinebulletins.RecognitionResult{Page: 1, Blocks: []onlinebulletins.RecognitionBlock{{Mode: "native", Role: "body_paragraph", Group: "article-1", Text: "", SourceIDs: []string{"p1-t1"}, Box: onlinebulletins.Box{X: .2, Y: .3, Width: .5, Height: .02}}}, UnassignedIDs: []string{}})
		json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": "gpt-6-luna-2026-09-22", "usage": map[string]any{"input_tokens": 100, "output_tokens": 40}, "output": []any{map[string]any{"type": "message", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": string(result)}}}}})
	}))
	defer server.Close()
	env := func(key string) string {
		switch key {
		case "AZURE_OPENAI_ENDPOINT":
			return server.URL
		case "AZURE_OPENAI_DEPLOYMENT":
			return "candidate"
		case "AZURE_OPENAI_API_KEY":
			return "PRIVATE_KEY_SENTINEL"
		}
		return ""
	}
	var output bytes.Buffer
	dir := filepath.Join(root, "results")
	if err := run(context.Background(), []string{"run", "--bundle", bundle, "--output", dir, "--repeat", "2"}, &output, env, server.Client(), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "PRIVATE_") {
		t.Fatal("private data reached stdout")
	}
	metrics, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil || strings.Contains(string(metrics), "PRIVATE_") {
		t.Fatal("missing or unsafe metrics", err)
	}
	var report evaluationReport
	if json.Unmarshal(metrics, &report) != nil || report.Requests != 2 || report.Passed != 2 || report.InputTokens != 200 || report.OutputTokens != 80 {
		t.Fatal("incomplete evaluation accounting", string(metrics))
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 3 {
		t.Fatal("expected private results and metrics", len(files))
	}
	for _, file := range files {
		info, _ := file.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatal("result permissions", info.Mode())
		}
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0700 {
		t.Fatal("directory permissions", info.Mode())
	}
	if err := run(context.Background(), []string{"run", "--bundle", bundle, "--output", dir}, io.Discard, env, server.Client(), nil); err == nil {
		t.Fatal("existing evaluation overwritten")
	}
}

func TestPrepareEvaluationBundleDoesNotNeedProviderCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.json")
	var output bytes.Buffer
	prepare := func(context.Context, string) ([]onlinebulletins.RecognitionPage, error) { return testPages(t), nil }
	if err := run(context.Background(), []string{"prepare", "--source", "private.pdf", "--bundle", path}, &output, func(string) string { t.Error("prepare read credentials"); return "" }, nil, prepare); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "PRIVATE_") {
		t.Fatal("private source printed")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("bundle permissions", err)
	}
	if err := run(context.Background(), []string{"prepare", "--source", "private.pdf", "--bundle", path}, io.Discard, func(string) string { return "" }, nil, prepare); err == nil {
		t.Fatal("bundle overwritten")
	}
}

func TestEvaluationRejectsInvalidArgumentsWithoutProviderAccess(t *testing.T) {
	for _, args := range [][]string{{}, {"run", "--repeat", "3"}, {"run", "--repeat", "0"}, {"prepare"}, {"run", "--bundle", "missing"}, {"unknown"}} {
		if err := run(context.Background(), args, io.Discard, func(string) string { return "" }, nil, nil); err == nil {
			t.Fatal("invalid input accepted", args)
		}
	}
}

func TestVerifiedEvaluationIncludesCanonicalContextAndAllCalls(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle.json")
	writeBundle(t, bundle, testPages(t))
	canonical := filepath.Join(root, "canonical.json")
	if err := os.WriteFile(canonical, []byte(`{"title":"PRIVATE_TITLE","subtitle":"","issueNumber":1746,"date":"2026-11-08"}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, _ := io.ReadAll(r.Body)
		if !bytes.Contains(data, []byte("PRIVATE_TITLE")) {
			t.Error("canonical context missing")
		}
		result, _ := json.Marshal(onlinebulletins.RecognitionResult{Page: 1, Blocks: []onlinebulletins.RecognitionBlock{{Mode: "native", Role: "cover_verse", Group: "verse", SourceIDs: []string{"p1-t1"}, Box: onlinebulletins.Box{X: .2, Y: .3, Width: .5, Height: .02}}}, UnassignedIDs: []string{}})
		json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": "candidate", "usage": map[string]int{"input_tokens": 100, "output_tokens": 40}, "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": string(result)}}}}})
	}))
	defer server.Close()
	env := func(key string) string {
		switch key {
		case "AZURE_OPENAI_ENDPOINT":
			return server.URL
		case "AZURE_OPENAI_DEPLOYMENT":
			return "candidate"
		case "AZURE_OPENAI_API_KEY":
			return "key"
		}
		return ""
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"run", "--bundle", bundle, "--canonical", canonical, "--verify", "--output", filepath.Join(root, "results")}, &out, env, server.Client(), nil); err != nil {
		t.Fatal(err)
	}
	var report evaluationReport
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Pages != 1 || report.Requests != 2 || calls != 2 || report.InputTokens != 200 || strings.Contains(out.String(), "PRIVATE_") {
		t.Fatal("verification accounting/context unsafe", out.String(), calls)
	}
}
