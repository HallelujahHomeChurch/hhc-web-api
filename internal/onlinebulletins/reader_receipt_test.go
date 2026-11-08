package onlinebulletins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMemberProjectionExcludesSourceAndRetainsAnchors(t *testing.T) {
	source := validDocument(12)
	source.Components[0].Cover.Welcome[0].Sentences[0].Source = &SourceLocation{Page: 1, Box: Box{X: .1, Y: .1, Width: .3, Height: .1}}
	content, err := ProjectMemberContent(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(raw, &projected); err != nil {
		t.Fatal(err)
	}
	if projected["printedBodyPageCount"] != float64(source.SourcePageCount-2) {
		t.Fatal("printed body summary lost")
	}
	for _, forbidden := range []string{`"source":`, `sourceAssetChecksum`, `sourcePageCount`, `reviewIssues`, `createdBy`, `extractorVersion`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("private field: %s", forbidden)
		}
	}
	if source.Components[0].Cover.Welcome[0].Sentences[0].Source == nil {
		t.Fatal("projection mutated history")
	}
	if content.Components[0].Cover.Welcome[0].Sentences[0].ID != source.Components[0].Cover.Welcome[0].Sentences[0].ID {
		t.Fatal("anchor changed")
	}
	if len(content.Pages) != 12 || len(content.LayoutManifest.Pages) != 12 {
		t.Fatal("layout lost")
	}
}

func TestMemberHeaderSpeakerStartUsesVerifiedSourceBeforeRedaction(t *testing.T) {
	for _, name := range []string{"known", "explicit", "multiple-header-speakers", "missing-source", "unknown-renderer"} {
		t.Run(name, func(t *testing.T) {
			source := validDocument(12)
			source.LayoutManifest.RendererVersion = "v10"
			body := source.Components[1].BodySection
			body.Contributors = nil
			location := &SourceLocation{Page: 2, Box: Box{X: .1, Y: .2, Width: .3, Height: .02}}
			body.Title.Sentences[0].Source = location
			person := Contributor{Role: "speaker", Name: body.Title}
			body.Header = &BodyHeader{LectureDate: body.Title, Contributors: []Contributor{person}}
			switch name {
			case "explicit":
				body.Contributors = []Contributor{person}
			case "multiple-header-speakers":
				body.Header.Contributors = append(body.Header.Contributors, person)
			case "missing-source":
				body.Header.Contributors[0].Name.Sentences = nil
			case "unknown-renderer":
				source.LayoutManifest.RendererVersion = "v8"
			}
			content, err := ProjectMemberContent(source)
			if err != nil {
				t.Fatal(err)
			}
			expected := ""
			if name == "known" {
				expected = source.Components[1].ID
			}
			if content.HeaderSpeakerStartComponentID != expected {
				t.Fatal("unverified start", content.HeaderSpeakerStartComponentID)
			}
			if content.Components[1].BodySection.Title.Sentences[0].Source != nil {
				t.Fatal("private source leaked")
			}
		})
	}
}
