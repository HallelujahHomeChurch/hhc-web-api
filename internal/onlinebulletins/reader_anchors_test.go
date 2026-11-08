package onlinebulletins

import "testing"

func TestReaderAnchorsUseTypedOrderAndPublishedMetadata(t *testing.T) {
	d := validDocument(12)
	d.LayoutManifest.Pages[0].FixedSlots = []FixedSlot{{Element: "title"}}
	anchors := ReaderAnchors(d, CanonicalMetadata{Title: "刊登的標題"})
	if len(anchors) != 14 || anchors[0].ID != "canonical-title" || anchors[0].Text != "刊登的標題" || anchors[1].ID != "sentence-welcome" {
		t.Fatal("wrong typed reading order", anchors)
	}
	for _, a := range anchors {
		if a.ID == "" || a.ComponentID == "" || a.PageID == "" || a.Text == "" {
			t.Fatal("unanchored text", a)
		}
	}
}

func TestReaderCanonicalQuotesMatchVisiblePaperText(t *testing.T) {
	d := validDocument(12)
	d.LayoutManifest.Pages[0].FixedSlots = []FixedSlot{{Element: "issueNumber"}, {Element: "date"}}
	anchors := ReaderAnchors(d, CanonicalMetadata{IssueNumber: 1739, Date: "2026-09-20"})
	if anchors[0].Text != "第1739期" || anchors[1].Text != "Sep.20.2026" {
		t.Fatal("canonical quote differs from visible paper", anchors[:2])
	}
}
