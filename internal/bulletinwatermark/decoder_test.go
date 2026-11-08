package bulletinwatermark

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecoderProcess(t *testing.T) {
	root := ""
	for index, arg := range os.Args {
		if arg == "--job-root" && index+1 < len(os.Args) {
			root = os.Args[index+1]
		}
	}
	if root == "" {
		return
	}
	starts := filepath.Join(root, "decoder-starts")
	count := 0
	if value, err := os.ReadFile(starts); err == nil {
		count, _ = strconv.Atoi(string(value))
	}
	_ = os.WriteFile(starts, []byte(strconv.Itoa(count+1)), 0600)
	for {
		var request struct {
			Job                string `json:"job"`
			EvaluationIdentity string `json:"evaluationIdentity"`
		}
		if err := readDecoderFrame(os.Stdin, &request); err != nil {
			return
		}
		image, err := os.ReadFile(filepath.Join(root, request.Job, "input"))
		if err != nil || request.EvaluationIdentity != DirectDecoderEvaluationIdentity {
			return
		}
		switch {
		case strings.Contains(string(image), "block"):
			time.Sleep(5 * time.Second)
		case strings.Contains(string(image), "slow"):
			time.Sleep(150 * time.Millisecond)
		case strings.Contains(string(image), "malformed"):
			_, _ = os.Stdout.Write([]byte{0, 0, 0, 1, '{'})
			continue
		case strings.Contains(string(image), "oversized"):
			var header [4]byte
			binary.BigEndian.PutUint32(header[:], decoderFrameLimit+1)
			_, _ = os.Stdout.Write(header[:])
			continue
		}
		response := map[string]any{"status": "decoded", "payload": strings.Repeat("1", 61), "quality": .95, "evaluationIdentity": DirectDecoderEvaluationIdentity}
		if strings.Contains(string(image), "inconclusive") {
			response = map[string]any{"status": "inconclusive", "quality": .7, "evaluationIdentity": DirectDecoderEvaluationIdentity}
		} else if strings.Contains(string(image), "low-quality") {
			response["quality"] = .899
		} else if strings.Contains(string(image), "boundary-quality") {
			response["quality"] = .9
		} else if strings.Contains(string(image), "missing-quality") {
			delete(response, "quality")
		}
		body, _ := json.Marshal(response)
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(body)))
		_, _ = os.Stdout.Write(append(header[:], body...))
	}
}

func decoderFixture(t *testing.T) (*Runner, string) {
	t.Helper()
	binary, _ := os.Executable()
	script := filepath.Join(t.TempDir(), "decoder-helper")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec "+quote(binary)+" -test.run=TestDecoderProcess -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "private")
	runner, err := NewRunner(script, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runner.Close() })
	return runner, filepath.Join(root, "decoder-starts")
}

func decoderImage(marker string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), marker...)
}

func TestDecoderIsLazyReusableAndDefaultOff(t *testing.T) {
	runner, starts := decoderFixture(t)
	if result, err := runner.Decode(context.Background(), decoderImage("disabled")); err != nil || result.Accepted || result.Payload != "" || result.Quality != 0 {
		t.Fatalf("disabled decode: result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(starts); !os.IsNotExist(err) {
		t.Fatal("disabled decoder started a child")
	}
	runner.ConfigureDirectDecoder(true)
	for _, marker := range []string{"first", "inconclusive"} {
		result, err := runner.Decode(context.Background(), decoderImage(marker))
		if err != nil || result.Accepted != (marker == "first") || result.Accepted && len(result.Payload) != 61 || result.Quality != map[string]float64{"first": .95, "inconclusive": .7}[marker] {
			t.Fatalf("decode %s: result=%+v err=%v", marker, result, err)
		}
	}
	if value, _ := os.ReadFile(starts); string(value) != "1" {
		t.Fatalf("child starts=%q", value)
	}
}

func TestDecoderProtocolFailureAndCancellationRestart(t *testing.T) {
	for _, marker := range []string{"malformed", "oversized", "block"} {
		t.Run(marker, func(t *testing.T) {
			runner, starts := decoderFixture(t)
			runner.ConfigureDirectDecoder(true)
			ctx := context.Background()
			if marker == "block" {
				if result, err := runner.Decode(ctx, decoderImage("warm")); err != nil || !result.Accepted {
					t.Fatal("decoder did not warm", err)
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			if _, err := runner.Decode(ctx, decoderImage(marker)); err == nil {
				t.Fatal("protocol failure accepted")
			}
			if result, err := runner.Decode(context.Background(), decoderImage("restart")); err != nil || !result.Accepted {
				t.Fatal("decoder did not restart", err)
			}
			if value, _ := os.ReadFile(starts); string(value) != "2" {
				t.Fatalf("child starts=%q", value)
			}
		})
	}
}

func TestDecoderRejectsMissingQuality(t *testing.T) {
	runner, _ := decoderFixture(t)
	runner.ConfigureDirectDecoder(true)
	if _, err := runner.Decode(context.Background(), decoderImage("missing-quality")); err == nil {
		t.Fatal("decoder accepted a response without quality")
	}
}

func TestDecoderEnforcesFrozenDecodedQuality(t *testing.T) {
	runner, _ := decoderFixture(t)
	runner.ConfigureDirectDecoder(true)
	if _, err := runner.Decode(context.Background(), decoderImage("low-quality")); err == nil {
		t.Fatal("decoder accepted quality below the frozen threshold")
	}
	result, err := runner.Decode(context.Background(), decoderImage("boundary-quality"))
	if err != nil || !result.Accepted || result.Quality != .9 {
		t.Fatalf("boundary decode: result=%+v err=%v", result, err)
	}
}

func TestDecoderSerializesRequestsAndCloseIsTerminal(t *testing.T) {
	runner, _ := decoderFixture(t)
	runner.ConfigureDirectDecoder(true)
	started := time.Now()
	var wait sync.WaitGroup
	wait.Add(2)
	for range 2 {
		go func() {
			defer wait.Done()
			_, _ = runner.Decode(context.Background(), decoderImage("slow"))
		}()
	}
	wait.Wait()
	if elapsed := time.Since(started); elapsed < 280*time.Millisecond {
		t.Fatalf("concurrent decode elapsed=%s", elapsed)
	}
	if err := runner.decoder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Decode(context.Background(), decoderImage("after-close")); err == nil {
		t.Fatal("closed decoder restarted")
	}
}

var _ Decoder = (*framedDecoder)(nil)
