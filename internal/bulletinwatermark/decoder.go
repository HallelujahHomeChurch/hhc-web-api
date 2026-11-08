package bulletinwatermark

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const DirectDecoderEvaluationIdentity = "trustmark-q1-bch5-logitmean-rot4-v1"
const DirectDecoderMinimumQuality = .9
const decoderFrameLimit = 64 << 10
const decoderDeadline = 30 * time.Second

type Decoder interface {
	Decode(context.Context, []byte) (DecodeResult, error)
	Close() error
}

type DecodeResult struct {
	Payload  string
	Quality  float64
	Accepted bool
}

type framedDecoder struct {
	executable, root string
	mu               sync.Mutex
	cmd              *exec.Cmd
	stdin            io.WriteCloser
	stdout           *bufio.Reader
	done             chan error
	closed           bool
}

func newFramedDecoder(executable, root string) Decoder {
	return &framedDecoder{executable: executable, root: root}
}

func (d *framedDecoder) Decode(ctx context.Context, image []byte) (DecodeResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return DecodeResult{}, ErrUnavailable
	}
	if len(image) == 0 || len(image) > MaxEvidenceBytes || (evidenceKind(image) != EvidencePNG && evidenceKind(image) != EvidenceJPEG) {
		return DecodeResult{}, ErrInvalidInvestigation
	}
	dir, err := os.MkdirTemp(d.root, "decode-")
	if err != nil {
		return DecodeResult{}, err
	}
	defer os.RemoveAll(dir)
	if err = os.WriteFile(filepath.Join(dir, "input"), image, 0600); err != nil {
		return DecodeResult{}, err
	}
	if d.cmd == nil {
		if err = d.start(); err != nil {
			return DecodeResult{}, err
		}
	}
	request, _ := json.Marshal(map[string]string{"job": filepath.Base(dir), "evaluationIdentity": DirectDecoderEvaluationIdentity})
	processCtx, cancel := context.WithTimeout(ctx, decoderDeadline)
	defer cancel()
	type result struct {
		response struct {
			Status             string   `json:"status"`
			Payload            string   `json:"payload"`
			Quality            *float64 `json:"quality"`
			EvaluationIdentity string   `json:"evaluationIdentity"`
		}
		err error
	}
	completed := make(chan result, 1)
	go func() {
		var value result
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(request)))
		if _, value.err = io.Copy(d.stdin, bytes.NewReader(append(header[:], request...))); value.err == nil {
			value.err = readDecoderFrame(d.stdout, &value.response)
		}
		completed <- value
	}()
	select {
	case <-processCtx.Done():
		d.stop()
		<-completed
		return DecodeResult{}, ErrUnavailable
	case value := <-completed:
		if value.err != nil || value.response.EvaluationIdentity != DirectDecoderEvaluationIdentity {
			d.stop()
			return DecodeResult{}, ErrUnavailable
		}
		switch value.response.Status {
		case "decoded":
			if len(value.response.Payload) != 61 || strings.Trim(value.response.Payload, "01") != "" || value.response.Quality == nil || *value.response.Quality < DirectDecoderMinimumQuality || *value.response.Quality > 1 {
				d.stop()
				return DecodeResult{}, ErrUnavailable
			}
			return DecodeResult{Payload: value.response.Payload, Quality: *value.response.Quality, Accepted: true}, nil
		case "inconclusive":
			if value.response.Payload != "" || value.response.Quality == nil || *value.response.Quality < .5 || *value.response.Quality > 1 {
				d.stop()
				return DecodeResult{}, ErrUnavailable
			}
			return DecodeResult{Quality: *value.response.Quality}, nil
		default:
			d.stop()
			return DecodeResult{}, ErrUnavailable
		}
	}
}

func (d *framedDecoder) start() error {
	cmd := exec.Command(d.executable, "--decode-server", "--job-root", d.root)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + d.root, "TMPDIR=" + d.root, "PYTHONDONTWRITEBYTECODE=1", "HHC_WATERMARK_MODEL_DIR=/opt/watermark/models"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ErrUnavailable
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return ErrUnavailable
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		stdin.Close()
		return ErrUnavailable
	}
	d.cmd, d.stdin, d.stdout, d.done = cmd, stdin, bufio.NewReader(stdout), make(chan error, 1)
	go func() { d.done <- cmd.Wait() }()
	return nil
}

func readDecoderFrame(reader io.Reader, target any) error {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > decoderFrameLimit {
		return ErrUnavailable
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrUnavailable
	}
	return nil
}

func (d *framedDecoder) stop() {
	if d.cmd == nil {
		return
	}
	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
	<-d.done
	d.cmd, d.stdin, d.stdout, d.done = nil, nil, nil, nil
}

func (d *framedDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	d.stop()
	return nil
}
