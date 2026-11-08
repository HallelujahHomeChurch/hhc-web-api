package bulletinwatermark

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type PDFScopeHint struct {
	Version         int    `json:"version"`
	IssueID         string `json:"issueId"`
	Series          string `json:"series"`
	Locale          string `json:"locale"`
	Revision        int64  `json:"revision"`
	SourceSHA256    string `json:"sourceSha256"`
	RendererVersion string `json:"rendererVersion"`
}

type PDFQuery struct {
	Images    [][]byte
	ScopeHint *PDFScopeHint
}

func (h PDFScopeHint) valid() bool {
	issue, err := uuid.Parse(h.IssueID)
	if err != nil || issue.String() != h.IssueID || len(h.RendererVersion) == 0 || len(h.RendererVersion) > 200 {
		return false
	}
	for _, value := range h.RendererVersion {
		if (value < '0' || value > '9') && (value < 'A' || value > 'Z') && (value < 'a' || value > 'z') && !strings.ContainsRune("._:+-", value) {
			return false
		}
	}
	return h.Version == 1 && ValidEdition(h.Series, h.Locale) &&
		h.Revision > 0 && len(h.SourceSHA256) == 64 && strings.Trim(h.SourceSHA256, "0123456789abcdef") == ""
}

func (h PDFScopeHint) CompatibleScope(scopes []CandidateScope) (CandidateScope, bool) {
	if !h.valid() {
		return CandidateScope{}, false
	}
	for _, scope := range scopes {
		if h.IssueID == scope.IssueID && NormalizeSeries(h.Series) == NormalizeSeries(scope.Series) && h.Locale == scope.Locale && h.Revision == scope.Revision && h.SourceSHA256 == scope.SourceSHA256 && h.RendererVersion == scope.RendererVersion {
			return scope, true
		}
	}
	return CandidateScope{}, false
}

func (r *Runner) QueryPDF(ctx context.Context, source []byte) (PDFQuery, error) {
	if r == nil || len(source) == 0 || int64(len(source)) > MaxInputBytes {
		return PDFQuery{}, ErrUnavailable
	}
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return PDFQuery{}, ctx.Err()
	}
	defer func() { <-r.slots }()
	dir, err := os.MkdirTemp(r.root, "render-")
	if err != nil {
		return PDFQuery{}, err
	}
	defer os.RemoveAll(dir)
	if err = os.WriteFile(filepath.Join(dir, "input.pdf"), source, 0600); err != nil {
		return PDFQuery{}, err
	}
	var result struct {
		Status string        `json:"status"`
		Images []string      `json:"images"`
		Hint   *PDFScopeHint `json:"scope_hint"`
	}
	if err = r.run(ctx, dir, map[string]string{"operation": "query_pdf", "source": "input.pdf"}, &result); err != nil || result.Status != "query_pdf" || len(result.Images) == 0 || len(result.Images) > MaxPages {
		return PDFQuery{}, ErrUnavailable
	}
	if result.Hint != nil && !result.Hint.valid() {
		return PDFQuery{}, ErrUnavailable
	}
	images := make([][]byte, len(result.Images))
	total := 0
	for i, name := range result.Images {
		if name != filepath.Base(name) {
			return PDFQuery{}, ErrUnavailable
		}
		images[i], err = os.ReadFile(filepath.Join(dir, name))
		total += len(images[i])
		if err != nil || evidenceKind(images[i]) != EvidencePNG || total > 256<<20 {
			return PDFQuery{}, ErrUnavailable
		}
	}
	return PDFQuery{Images: images, ScopeHint: result.Hint}, nil
}
