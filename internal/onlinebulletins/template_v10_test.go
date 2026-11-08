package onlinebulletins

import (
	"reflect"
	"testing"
)

func TestV10CandidateRetainsSnapshotAndCurrentProfile(t *testing.T) {
	source, canonical, manifest, snapshot := v9CandidateSource()
	manifest.RendererVersion = "v10"
	candidate, issues, err := ApplyTemplateV10(source, canonical, manifest, snapshot)
	if err != nil || candidate.Document == nil || candidate.Document.LayoutManifest.RendererVersion != "v10" || candidate.Document.TemplateSnapshot == nil || *candidate.Document.TemplateSnapshot != snapshot {
		t.Fatal("V10 lost source identity or snapshot", err)
	}
	manifest.RendererVersion = "v9"
	_, priorIssues, err := ApplyTemplateV9(source, canonical, manifest, snapshot)
	if err != nil || !reflect.DeepEqual(issues, priorIssues) {
		t.Fatal("V10 changes unrelated source classification", err)
	}
	manifest.RendererVersion = "v10"
	for _, version := range []int64{0, 9007199254740992} {
		invalid := snapshot
		invalid.Version = version
		if _, _, err := ApplyTemplateV10(source, canonical, manifest, invalid); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}
