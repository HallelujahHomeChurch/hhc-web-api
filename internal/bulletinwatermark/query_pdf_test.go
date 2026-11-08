package bulletinwatermark

import "testing"

func TestPDFScopeHintMatchesOnlyAnExactSelectedScope(t *testing.T) {
	scope := CandidateScope{
		IssueID: "00000000-0000-4000-8000-000000000001", Locale: "zh-Hant", Revision: 7,
		SourceSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RendererVersion: "trustmark-q-bch5-pdf-1",
	}
	hint := PDFScopeHint{Version: 1, IssueID: scope.IssueID, Locale: scope.Locale, Revision: scope.Revision, SourceSHA256: scope.SourceSHA256, RendererVersion: scope.RendererVersion}
	if got, ok := hint.CompatibleScope([]CandidateScope{scope}); !ok || got != scope {
		t.Fatal("valid hint did not match its selected scope")
	}
	for _, changed := range []PDFScopeHint{
		{Version: 2, IssueID: hint.IssueID, Locale: hint.Locale, Revision: hint.Revision, SourceSHA256: hint.SourceSHA256, RendererVersion: hint.RendererVersion},
		{Version: 1, IssueID: "00000000-0000-4000-8000-000000000002", Locale: hint.Locale, Revision: hint.Revision, SourceSHA256: hint.SourceSHA256, RendererVersion: hint.RendererVersion},
		{Version: 1, IssueID: hint.IssueID, Locale: hint.Locale, Revision: hint.Revision, SourceSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RendererVersion: hint.RendererVersion},
	} {
		if _, ok := changed.CompatibleScope([]CandidateScope{scope}); ok {
			t.Fatalf("contradictory hint matched: %+v", changed)
		}
	}
}
