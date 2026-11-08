package onlinebulletins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"
	"time"
)

var ErrLayoutRunner = errors.New("layout_runner_failed")

// Paths and artifact identity are owned by the immutable extractor image, never
// a document or browser request. The child receives no service credentials.
type LayoutRunner struct {
	NodePath, ScriptPath, AssetsDirectory, RendererArtifactSHA256 string
}

type LayoutResult struct {
	Document                          Document
	SubmissionJSON, MeasurementJSON   json.RawMessage
	ContentHash, LayoutValidationHash string
}

func (r LayoutRunner) Compose(ctx context.Context, document Document, metadata CanonicalMetadata) (LayoutResult, error) {
	return r.run(ctx, document, metadata, true)
}

func (r LayoutRunner) Validate(ctx context.Context, document Document, metadata CanonicalMetadata) (LayoutResult, error) {
	return r.run(ctx, document, metadata, false)
}

// VerifyComposedLayout rechecks a worker result against the stored revision at
// the persistence boundary. Layout changes are allowed; semantic changes are not.
func VerifyComposedLayout(result LayoutResult, document Document, metadata CanonicalMetadata) (LayoutResult, error) {
	output, err := json.Marshal(struct {
		SubmissionJSON      string          `json:"submissionJSON"`
		ExpectedContentHash string          `json:"expectedContentHash"`
		Measurement         json.RawMessage `json:"measurement"`
	}{string(result.SubmissionJSON), result.ContentHash, result.MeasurementJSON})
	if err != nil {
		return LayoutResult{}, ErrLayoutRunner
	}
	checked, err := verifyLayoutResult(output, document, metadata, true)
	if err != nil || checked.LayoutValidationHash != result.LayoutValidationHash {
		return LayoutResult{}, ErrLayoutRunner
	}
	canonical, _, err := MarshalLayoutSubmission(checked.Document, metadata)
	if err != nil || !bytes.Equal(canonical, checked.SubmissionJSON) {
		return LayoutResult{}, ErrLayoutRunner
	}
	return checked, nil
}

func (r LayoutRunner) run(parent context.Context, document Document, metadata CanonicalMetadata, compose bool) (LayoutResult, error) {
	fail := func() (LayoutResult, error) { return LayoutResult{}, ErrLayoutRunner }
	if !filepath.IsAbs(r.NodePath) || !filepath.IsAbs(r.ScriptPath) || !filepath.IsAbs(r.AssetsDirectory) || !hashPattern.MatchString(r.RendererArtifactSHA256) || document.LayoutManifest.RendererArtifactSha256 != r.RendererArtifactSHA256 {
		return fail()
	}
	if _, err := ValidateDocument(document); err != nil {
		return fail()
	}
	document.LayoutManifest.ContentHash, document.LayoutManifest.LayoutValidationHash = "", ""
	submission, contentHash, err := MarshalLayoutSubmission(document, metadata)
	if err != nil || len(submission) > 8<<20 {
		return fail()
	}
	input, err := json.Marshal(struct {
		SubmissionJSON      string `json:"submissionJSON"`
		ExpectedContentHash string `json:"expectedContentHash"`
		TimeoutMS           int    `json:"timeoutMs"`
	}{string(submission), contentHash, 60000})
	if err != nil || len(input) > 16<<20 {
		return fail()
	}
	directory, err := os.MkdirTemp("", "hhc-bulletin-layout-")
	if err != nil {
		return fail()
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "input.json")
	if os.WriteFile(path, input, 0600) != nil {
		return fail()
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	args := []string{r.ScriptPath}
	if compose {
		args = []string{"/opt/bulletin-layout/compose.mjs", r.ScriptPath}
	}
	args = append(args, path, r.AssetsDirectory)
	command := exec.CommandContext(ctx, r.NodePath, args...)
	command.Dir = directory
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + directory, "TMPDIR=" + directory, "PLAYWRIGHT_BROWSERS_PATH=/opt/browsers"}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	stdout, stderr := &limitedProcessOutput{limit: 16 << 20}, &limitedProcessOutput{}
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if err != nil || stdout.overflow || stderr.overflow {
		var exitError *exec.ExitError
		if compose && ctx.Err() == nil && !stdout.overflow && !stderr.overflow && errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return LayoutResult{}, layoutFailure(stdout.Bytes(), document, contentHash)
		}
		return LayoutResult{}, errors.Join(ErrLayoutRunner, ctx.Err())
	}
	output := stdout.Bytes()
	if !compose {
		output, err = json.Marshal(struct {
			SubmissionJSON      string          `json:"submissionJSON"`
			ExpectedContentHash string          `json:"expectedContentHash"`
			Measurement         json.RawMessage `json:"measurement"`
		}{string(submission), contentHash, output})
		if err != nil {
			return fail()
		}
	}
	checked, err := verifyLayoutResult(output, document, metadata, compose)
	if err != nil {
		return LayoutResult{}, err
	}
	if compose {
		// JS composition can change property order or materialize optional zeroes.
		// Measure the Go-serialized saved document, not those intermediate bytes,
		// so database round trips and publication verify the same content identity.
		cancel()
		return r.run(parent, checked.Document, metadata, false)
	}
	return checked, nil
}

func verifyLayoutResult(output []byte, original Document, metadata CanonicalMetadata, compose bool) (LayoutResult, error) {
	fail := func() (LayoutResult, error) { return LayoutResult{}, ErrLayoutRunner }
	var envelope struct {
		SubmissionJSON      string          `json:"submissionJSON"`
		ExpectedContentHash string          `json:"expectedContentHash"`
		Measurement         json.RawMessage `json:"measurement"`
	}
	if len(output) > 16<<20 || decodeLayoutJSON(output, &envelope) != nil || len(envelope.SubmissionJSON) > 8<<20 {
		return fail()
	}
	digest := sha256.Sum256([]byte(envelope.SubmissionJSON))
	if hex.EncodeToString(digest[:]) != envelope.ExpectedContentHash {
		return fail()
	}
	var submission layoutSubmission
	if decodeLayoutJSON([]byte(envelope.SubmissionJSON), &submission) != nil || submission.CanonicalMetadata != metadata {
		return fail()
	}
	document := submission.Document
	if _, err := ValidateDocument(document); err != nil {
		return fail()
	}
	if document.LayoutManifest.ContentHash != "" || document.LayoutManifest.LayoutValidationHash != "" || document.LayoutManifest.RendererArtifactSha256 != original.LayoutManifest.RendererArtifactSha256 || !reflect.DeepEqual(document.LayoutManifest.Assets, original.LayoutManifest.Assets) {
		return fail()
	}
	// Composition changes presentation only, within readable bounds. Source page
	// identity, fragment membership, text, anchors and provenance are immutable.
	left, right := original, document
	left.LayoutManifest.ContentHash, left.LayoutManifest.LayoutValidationHash = "", ""
	if compose {
		if !validComposedPresentation(original, document) {
			return fail()
		}
		left.LayoutManifest, right.LayoutManifest = LayoutManifest{}, LayoutManifest{}
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	var l, r any
	if json.Unmarshal(leftJSON, &l) != nil || json.Unmarshal(rightJSON, &r) != nil {
		return fail()
	}
	if compose {
		clearLayoutSpacing(l)
		clearLayoutSpacing(r)
	}
	if !reflect.DeepEqual(l, r) {
		return fail()
	}
	var proof struct {
		ContentHash            string   `json:"contentHash"`
		RendererArtifactSHA256 string   `json:"rendererArtifactSha256"`
		ChromiumVersion        string   `json:"chromiumVersion"`
		FontHashes             []string `json:"fontHashes"`
		Pages                  []struct {
			PageID string          `json:"pageId"`
			Width  float64         `json:"width"`
			Height float64         `json:"height"`
			Slots  json.RawMessage `json:"slots"`
		} `json:"pages"`
		Overflow             []json.RawMessage `json:"overflow"`
		LayoutValidationHash string            `json:"layoutValidationHash"`
	}
	if decodeLayoutJSON(envelope.Measurement, &proof) != nil || proof.ContentHash != envelope.ExpectedContentHash || proof.RendererArtifactSHA256 != document.LayoutManifest.RendererArtifactSha256 || proof.ChromiumVersion != "153.0.8010.12" || proof.Overflow == nil || len(proof.Overflow) != 0 || len(proof.Pages) != len(document.Pages) || !hashPattern.MatchString(proof.LayoutValidationHash) {
		return fail()
	}
	fonts := []string{}
	for _, asset := range document.LayoutManifest.Assets {
		if asset.Kind == "font" {
			fonts = append(fonts, asset.SHA256)
		}
	}
	slices.Sort(fonts)
	slices.Sort(proof.FontHashes)
	if !slices.Equal(fonts, proof.FontHashes) {
		return fail()
	}
	for i, page := range proof.Pages {
		// Chromium rounds CSS layout to subpixels; allow only that rounding in
		// PDF points, never a differently scaled or missing measurement surface.
		if page.PageID != document.Pages[i].ID || math.Abs(page.Width-document.Pages[i].Width) > .02 || math.Abs(page.Height-document.Pages[i].Height) > .02 || len(page.Slots) == 0 || page.Slots[0] != '[' {
			return fail()
		}
		expected := map[string]bool{}
		for _, slot := range document.LayoutManifest.Pages[i].Slots {
			expected[slot.ID] = true
		}
		for _, slot := range document.LayoutManifest.Pages[i].FixedSlots {
			expected[slot.ID] = true
		}
		var slots []struct {
			ID string `json:"slotId"`
		}
		if json.Unmarshal(page.Slots, &slots) != nil || len(slots) != len(expected) {
			return fail()
		}
		for _, slot := range slots {
			if !expected[slot.ID] {
				return fail()
			}
			delete(expected, slot.ID)
		}
	}
	// The runner appends this field last. Hash the original JSON bytes, avoiding
	// cross-language float formatting or key-order recanonicalization.
	raw := bytes.TrimSpace(envelope.Measurement)
	suffix := []byte(`,"layoutValidationHash":"` + proof.LayoutValidationHash + `"}`)
	if !bytes.HasSuffix(raw, suffix) {
		return fail()
	}
	unsigned := append(bytes.Clone(raw[:len(raw)-len(suffix)]), '}')
	validationHash := sha256.Sum256(unsigned)
	if hex.EncodeToString(validationHash[:]) != proof.LayoutValidationHash {
		return fail()
	}
	return LayoutResult{Document: document, SubmissionJSON: json.RawMessage(envelope.SubmissionJSON), MeasurementJSON: bytes.Clone(envelope.Measurement), ContentHash: proof.ContentHash, LayoutValidationHash: proof.LayoutValidationHash}, nil
}

func decodeLayoutJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrLayoutRunner
	}
	return nil
}

func clearLayoutSpacing(value any) {
	switch value := value.(type) {
	case map[string]any:
		if style, ok := value["style"].(map[string]any); ok {
			for _, key := range []string{"lineHeight", "letterSpacing", "fontSize", "align", "indent", "firstLineIndent", "spaceBefore", "spaceAfter"} {
				delete(style, key)
			}
		}
		if _, span := value["fontRole"]; span {
			delete(value, "fontSize")
		}
		for _, child := range value {
			clearLayoutSpacing(child)
		}
	case []any:
		for _, child := range value {
			clearLayoutSpacing(child)
		}
	}
}

func validComposedPresentation(original, composed Document) bool {
	if !reflect.DeepEqual(original.Pages, composed.Pages) || len(composed.Pages) != composed.SourcePageCount {
		return false
	}
	for i, page := range original.LayoutManifest.Pages {
		before, after := map[string]any{}, map[string]any{}
		for _, slot := range page.Slots {
			slot.Box = Box{}
			// Parser-only provenance is absent after the renderer JSON round trip.
			// The containing page and serialized fragment identities remain checked.
			slot.sourcePage = 0
			before[slot.ID] = slot
		}
		for _, slot := range composed.LayoutManifest.Pages[i].Slots {
			slot.Box = Box{}
			after[slot.ID] = slot
		}
		if !reflect.DeepEqual(before, after) {
			return false
		}
	}
	blocks := map[string]*Block{}
	for i := range original.Components {
		for _, block := range componentBlocks(&original.Components[i]) {
			blocks[block.ID] = block
		}
	}
	for i := range composed.Components {
		component := &composed.Components[i]
		for _, block := range componentBlocks(component) {
			before := blocks[block.ID]
			if before == nil || len(before.Sentences) != len(block.Sentences) {
				return false
			}
			minimum := math.Min(before.Style.FontSize, math.Max(12, before.Style.FontSize*.8))
			if component.Type == "cover" {
				minimum = math.Min(9, before.Style.FontSize)
			}
			if block.Style.FontSize < minimum || block.Style.FontSize > before.Style.FontSize {
				return false
			}
			scale := block.Style.FontSize / before.Style.FontSize
			for n, sentence := range block.Sentences {
				if len(sentence.Spans) != len(before.Sentences[n].Spans) {
					return false
				}
				for j, span := range sentence.Spans {
					old := before.Sentences[n].Spans[j].FontSize
					if (old == nil) != (span.FontSize == nil) {
						return false
					}
					if old != nil && math.Abs(*span.FontSize-*old*scale) > .001 {
						return false
					}
				}
			}
		}
	}
	return true
}
