package onlinebulletins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"
)

func TestRepairCatalogCannotRestoreExcludedSourceMaterial(t *testing.T) {
	source := GeometryDocument{SourceSHA256: strings.Repeat("a", 64), PopplerVersion: PopplerVersion}
	for page := 1; page <= 4; page++ {
		source.Pages = append(source.Pages, GeometryPage{Number: page, Width: templateV1Width, Height: templateV1Height})
	}
	last := &source.Pages[3]
	last.Texts = []GeometryText{
		{Text: "Allowed announcement", FontFamily: "MingLiU", FontSize: 12, Box: Box{X: .1, Y: .3, Width: .5, Height: .02}},
		{Text: "主日敬拜服事表", FontFamily: "MingLiU", FontSize: 12, Box: Box{X: .1, Y: .56, Width: .5, Height: .02}},
		{Text: "EXCLUDED PRIVATE ROW", FontFamily: "MingLiU", FontSize: 12, Box: Box{X: .1, Y: .7, Width: .5, Height: .02}},
	}
	last.Images = []GeometryImage{
		{Box: Box{X: .1, Y: .4, Width: .2, Height: .1}, SHA256: strings.Repeat("b", 64)},
		{Box: Box{X: .1, Y: .5, Width: .2, Height: .2}, SHA256: strings.Repeat("c", 64)},
		{Box: Box{X: .1, Y: .7, Width: .2, Height: .1}, SHA256: strings.Repeat("d", 64)},
	}
	allowed, _, err := filterTemplateV1Geometry(source)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := CatalogParts(allowed)
	if err != nil || len(parts) != 2 {
		t.Fatal("wrong allowed source parts", parts, err)
	}
	for _, part := range parts {
		if strings.Contains(part.Text, "PRIVATE") || part.Box.Y+part.Box.Height > .56 || part.imageSHA == strings.Repeat("c", 64) || part.imageSHA == strings.Repeat("d", 64) {
			t.Fatal("excluded or crossing source became restorable", part)
		}
	}
	before := parts[0].ID
	allowed.SourceSHA256 = strings.Repeat("e", 64)
	changed, err := CatalogParts(allowed)
	if err != nil || changed[0].ID == before {
		t.Fatal("repair identity survived a source replacement", err)
	}
}

func TestSourceLinkedRepairRetainsPagesAndRequiresFreshProof(t *testing.T) {
	current := validDocument(4)
	current.TemplateSnapshot = &TemplateSettings{Version: 1, TemplateText: TemplateText{VisionMission: "使命", VisionFellowship: "同行", VisionCommitment: "回應"}}
	current.LayoutManifest.RendererVersion = "v10"
	before, _ := json.Marshal(current)
	part := SourcePart{ID: strings.Repeat("d", 64), Kind: "image", Page: 2, Box: Box{X: .2, Y: .4, Width: .4, Height: .1}}
	input := SourceRepairInput{SourceChecksum: current.SourceAssetChecksum, PartID: part.ID, ComponentID: "body", AfterBlockID: "block-body", Alt: "原稿插圖"}
	repair := PreparedSourceRepair{Input: input, Part: part, RendererSHA256: RendererV11SHA256, Illustration: &Illustration{AssetID: "b08a80e8-6388-4166-9a2a-23d336f74b7b", SHA256: strings.Repeat("e", 64), SizeBytes: 128}}
	draft, err := ApplySourceRepair(current, repair)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current.Pages, draft.Pages) || draft.LayoutManifest.RendererVersion != "v11" || draft.LayoutManifest.ContentHash != "" || len(draft.Components[1].Illustrations) != 1 {
		t.Fatal("repair lost pages/image or retained proof")
	}
	after, _ := json.Marshal(current)
	if !bytes.Equal(before, after) {
		t.Fatal("immutable source changed")
	}
	components := append([]Component{}, draft.Components...)
	if _, err := EditComponents(draft, components); err == nil {
		t.Fatal("caller-provided image accepted")
	}
	components[1].Illustrations = nil
	edited, err := EditComponents(draft, components)
	if err != nil || !reflect.DeepEqual(edited.Components[1].Illustrations, draft.Components[1].Illustrations) {
		t.Fatal("ordinary text save lost server-owned picture", err)
	}
	removed := repair
	removed.Input.Remove, removed.Illustration = true, nil
	without, removeErr := ApplySourceRepair(draft, removed)
	if removeErr != nil || len(without.Components[1].Illustrations) != 0 || without.LayoutManifest.ContentHash != "" {
		t.Fatal("unavailable picture could not be removed", removeErr)
	}
	if _, err := ApplySourceRepair(current, removed); err == nil {
		t.Fatal("nonexistent picture removed")
	}
	content, err := ProjectMemberContent(draft)
	raw, _ := json.Marshal(content)
	if err != nil || bytes.Contains(raw, []byte(repair.Illustration.AssetID)) || content.IllustrationAssets[part.ID] != repair.Illustration.AssetID {
		t.Fatal("private asset reference escaped member projection", err)
	}
	for _, mutate := range []func(*PreparedSourceRepair){
		func(r *PreparedSourceRepair) { r.Input.SourceChecksum = strings.Repeat("f", 64) },
		func(r *PreparedSourceRepair) { r.Input.ComponentID = "cover" },
		func(r *PreparedSourceRepair) { r.Input.AfterBlockID = "block-welcome" },
		func(r *PreparedSourceRepair) { r.Illustration = &Illustration{PNG: "private-payload"} },
	} {
		invalid := repair
		mutate(&invalid)
		if _, err := ApplySourceRepair(current, invalid); err == nil {
			t.Fatal("unbound source repair accepted")
		}
	}
	text := repair
	text.Part.Kind, text.Part.Text, text.Illustration = "text", "補回𠮷安。", nil
	restored, err := ApplySourceRepair(current, text)
	if err != nil || len(restored.Components[1].BodySection.Blocks) != 2 {
		t.Fatal("source text was not restorable", err)
	}
}

func TestSourceImageReencodingPreservesPixelsAndDropsTrailingMetadata(t *testing.T) {
	pixels := image.NewRGBA(image.Rect(0, 0, 2, 2))
	pixels.Set(0, 0, color.RGBA{R: 20, G: 30, B: 40, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	raw := append(encoded.Bytes(), []byte("PRIVATE SOURCE METADATA")...)
	sum := sha256.Sum256(raw)
	clean, err := sourceImagePNG(bytes.NewReader(raw), hex.EncodeToString(sum[:]))
	if err != nil || bytes.Contains(clean, []byte("PRIVATE SOURCE METADATA")) {
		t.Fatal("source metadata survived", err)
	}
	decoded, err := png.Decode(bytes.NewReader(clean))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := decoded.At(0, 0).RGBA()
	wantR, wantG, wantB, wantA := pixels.At(0, 0).RGBA()
	if r != wantR || g != wantG || b != wantB || a != wantA {
		t.Fatal("meaningful pixels changed")
	}
	if _, err := sourceImagePNG(bytes.NewReader(raw), strings.Repeat("0", 64)); err == nil {
		t.Fatal("source image hash mismatch accepted")
	}
}
