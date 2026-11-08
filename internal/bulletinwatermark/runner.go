package bulletinwatermark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrUnavailable = errors.New("watermark rendering unavailable")

type Runner struct {
	executable, root string
	slots            chan struct{}
	lock             *os.File
	decoder          Decoder
}

func (r *Runner) ConfigureDirectDecoder(enabled bool) {
	if r == nil || !enabled || r.decoder != nil {
		return
	}
	r.decoder = newFramedDecoder(r.executable, r.root)
}

func (r *Runner) Decode(ctx context.Context, image []byte) (DecodeResult, error) {
	if r == nil || r.decoder == nil {
		return DecodeResult{}, nil
	}
	return r.decoder.Decode(ctx, image)
}

const MaxInputBytes int64 = 20 << 20
const MaxOutputBytes int64 = 20 << 20
const RenderDeadline = 180 * time.Second

type RenderMetadata struct {
	Status          string         `json:"status"`
	SourceSHA256    string         `json:"source_sha256"`
	OutputSHA256    string         `json:"output_sha256"`
	EngineVersion   string         `json:"engine_version"`
	RendererVersion string         `json:"pdf_engine_version"`
	ModelSHA256     string         `json:"model_sha256"`
	Pages           []PageMetadata `json:"pages"`
}

type PDFScopeMetadata struct {
	Version         int    `json:"version"`
	IssueID         string `json:"issueId"`
	Series          string `json:"series"`
	Locale          string `json:"locale"`
	Revision        int64  `json:"revision"`
	SourceSHA256    string `json:"sourceSha256"`
	RendererVersion string `json:"rendererVersion"`
}

func (m PDFScopeMetadata) valid() bool {
	return PDFScopeHint(m).valid()
}

type Output struct {
	Metadata RenderMetadata
	*os.File
	cleanup func()
	once    sync.Once
}

func (o *Output) Close() error {
	var err error
	o.once.Do(func() {
		err = o.File.Close()
		if o.cleanup != nil {
			o.cleanup()
		}
	})
	return err
}
func NewRunner(executable, root string) (*Runner, error) {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(root) {
		return nil, ErrUnavailable
	}
	var err error
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	lock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, ErrUnavailable
	}
	success := false
	defer func() {
		if !success {
			lock.Close()
		}
	}()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		_, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if entry.IsDir() && len(entry.Name()) > 7 && entry.Name()[:7] == "render-" {
			if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	info, err = os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, ErrUnavailable
	}
	success = true
	return &Runner{executable: executable, root: root, slots: make(chan struct{}, 1), lock: lock}, nil
}
func (r *Runner) Render(ctx context.Context, source io.Reader, payload string) (out *Output, err error) {
	return r.render(ctx, source, payload, nil, nil)
}

func (r *Runner) RenderProgress(ctx context.Context, source io.Reader, payload string, progress func(int)) (out *Output, err error) {
	return r.render(ctx, source, payload, nil, progress)
}

func (r *Runner) RenderScoped(ctx context.Context, source io.Reader, payload string, scope PDFScopeMetadata, progress func(int)) (out *Output, err error) {
	return r.render(ctx, source, payload, &scope, progress)
}

func (r *Runner) render(ctx context.Context, source io.Reader, payload string, scope *PDFScopeMetadata, progress func(int)) (out *Output, err error) {
	if r == nil {
		return nil, ErrUnavailable
	}
	if len(payload) != 61 || strings.Trim(payload, "01") != "" || scope != nil && !scope.valid() {
		return nil, ErrInvalidFingerprint
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return nil, ErrUnavailable
	}
	dir, err := os.MkdirTemp(r.root, "render-")
	if err != nil {
		<-r.slots
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir); <-r.slots }
	defer func() {
		if out == nil {
			cleanup()
		}
	}()
	input := filepath.Join(dir, "input.pdf")
	output := filepath.Join(dir, "output.pdf")
	f, err := os.OpenFile(input, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	sourceHash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(f, sourceHash), io.LimitReader(source, MaxInputBytes+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || size > MaxInputBytes {
		return nil, ErrUnavailable
	}
	markPhase(ctx, "source_read")
	processCtx, cancel := context.WithTimeout(ctx, RenderDeadline)
	defer cancel()
	request := map[string]any{"operation": "render_pdf", "source": "input.pdf", "output": "output.pdf", "payload": payload}
	if scope != nil {
		request["scope"] = scope
	}
	var metadata RenderMetadata
	if err = r.runWithProgress(processCtx, dir, request, &metadata, progress); err != nil {
		return nil, err
	}
	if request["operation"] == "render_pdf" {
		markHelperPhases(ctx, dir)
	}
	if metadata.Status != "encoded_pdf" || metadata.SourceSHA256 != hex.EncodeToString(sourceHash.Sum(nil)) || metadata.RendererVersion == "" || !strings.HasPrefix(metadata.RendererVersion, metadata.EngineVersion+"-pdf-") || metadata.EngineVersion == "" {
		return nil, ErrUnavailable
	}
	rec := Receipt{FingerprintPayload: payload, ModelSHA256: metadata.ModelSHA256, Pages: metadata.Pages}
	if rec.ValidateFingerprint() != nil {
		return nil, ErrUnavailable
	}

	f, err = os.Open(output)
	if err != nil {
		return nil, ErrUnavailable
	}
	info, err := f.Stat()
	if err != nil || info.Size() < 5 || info.Size() > MaxOutputBytes {
		f.Close()
		return nil, ErrUnavailable
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil || hex.EncodeToString(hash.Sum(nil)) != metadata.OutputSHA256 {
		f.Close()
		return nil, ErrUnavailable
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	markPhase(ctx, "validate")
	return &Output{File: f, Metadata: metadata, cleanup: cleanup}, nil
}

func (r *Runner) Close() error {
	if r == nil || r.lock == nil {
		return nil
	}
	if r.decoder != nil {
		_ = r.decoder.Close()
	}
	return r.lock.Close()
}

// run forwards neither environment credentials nor parser diagnostics. Killing the
// process group also stops a Poppler child when a caller cancels.
func (r *Runner) run(ctx context.Context, dir string, request any, result any) error {
	return r.runWithProgress(ctx, dir, request, result, nil)
}

func (r *Runner) runWithProgress(ctx context.Context, dir string, request any, result any, progress func(int)) error {
	data, err := json.Marshal(request)
	if err != nil {
		return ErrUnavailable
	}
	if err = os.WriteFile(filepath.Join(dir, "request.json"), data, 0600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, r.executable, "--job-dir", dir)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "PYTHONDONTWRITEBYTECODE=1", "HHC_WATERMARK_MODEL_DIR=/opt/watermark/models", "HHC_WATERMARK_PDFTOPPM=/usr/bin/pdftoppm"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stdout cappedOutput
	cmd.Stdout = &stdout
	if err = cmd.Start(); err != nil {
		return ErrUnavailable
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastProgress := 0
	for {
		select {
		case err = <-done:
			if err != nil {
				return ErrUnavailable
			}
			goto completed
		case <-ticker.C:
			if progress == nil {
				continue
			}
			value, readErr := os.ReadFile(filepath.Join(dir, "progress"))
			if readErr == nil {
				if percent, parseErr := strconv.Atoi(strings.TrimSpace(string(value))); parseErr == nil && percent >= 15 && percent <= 80 && percent > lastProgress {
					lastProgress = percent
					progress(percent)
				}
			}
		case <-ctx.Done():
			<-done
			return ErrUnavailable
		}
	}
completed:
	if err = json.Unmarshal(stdout.Bytes(), result); err != nil {
		return ErrUnavailable
	}
	return nil
}

type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, ErrUnavailable
	}
	return b.Buffer.Write(p)
}
