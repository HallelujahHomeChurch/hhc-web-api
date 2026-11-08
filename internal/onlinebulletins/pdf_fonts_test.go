package onlinebulletins

import "testing"

func TestEmbeddedFontEvidenceIsPageBoundAndRequiresConsistentRoles(t *testing.T) {
	doc := GeometryDocument{Pages: []GeometryPage{
		{Texts: []GeometryText{{FontFamily: "CIDFont+F1"}, {FontFamily: "CIDFont+F2"}, {FontFamily: "CIDFont+F3"}}},
		{Texts: []GeometryText{{FontFamily: "CIDFont+F1"}}},
	}}
	evidence := []byte(`[{"CIDFont+F1":["DFKai-SB"],"CIDFont+F2":["Undefined","全真顏體<"],"CIDFont+F3":["DFKai-SB","PMingLiU"]},{"CIDFont+F1":["Wingdings"]}]`)
	if err := restoreEmbeddedFontNames(&doc, evidence); err != nil {
		t.Fatal(err)
	}
	if doc.Pages[0].Texts[0].FontFamily != "DFKai-SB" || doc.Pages[0].Texts[1].FontFamily != "全真顏體<" || doc.Pages[0].Texts[2].FontFamily != "CIDFont+F3" || doc.Pages[1].Texts[0].FontFamily != "Wingdings" {
		t.Fatalf("font identity was lost or guessed: %+v", doc)
	}
	if role, known := sourceFontRole(doc.Pages[0].Texts[1].FontFamily); !known || role != "emphasis" {
		t.Fatal("embedded emphasis face not classified")
	}
	if restoreEmbeddedFontNames(&doc, []byte(`[]`)) == nil {
		t.Fatal("wrong page count accepted")
	}
}
