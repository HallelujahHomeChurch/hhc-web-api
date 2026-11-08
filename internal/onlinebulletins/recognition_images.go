package onlinebulletins

import "math"

// LLM selects the retained image; source capture alone supplies bytes and bounds.
func attachRecognitionImages(candidate *DocumentCandidate, results []RecognitionResult, images map[string]SourceImage) error {
	parts, err := CatalogParts(candidate.Evidence)
	if err != nil {
		return err
	}
	catalog := map[string]SourcePart{}
	for _, part := range parts {
		if part.Kind == "image" {
			catalog[part.ID] = part
		}
	}
	for _, result := range results {
		for _, block := range result.Blocks {
			if block.Role != "illustration" {
				continue
			}
			if len(block.ImageIDs) != 1 {
				return ErrRecognition
			}
			part, known := catalog[block.ImageIDs[0]]
			image, available := images[part.ID]
			if !known || !available || part.Page != result.Page {
				return ErrRecognition
			}
			page := candidate.Document.LayoutManifest.Pages[part.Page-1]
			var selected Slot
			distance := math.Inf(1)
			for _, slot := range page.Slots {
				d := math.Abs(slot.Box.Y+slot.Box.Height-part.Box.Y) + math.Abs(slot.Box.X-part.Box.X)
				if d < distance {
					selected, distance = slot, d
				}
			}
			if selected.ComponentID == "" {
				return ErrRecognition
			}
			document, err := ApplySourceRepair(*candidate.Document, PreparedSourceRepair{
				Input: SourceRepairInput{SourceChecksum: candidate.Document.SourceAssetChecksum, PartID: part.ID, ComponentID: selected.ComponentID, AfterBlockID: selected.BlockID, Alt: "原週報插圖"}, Part: part,
				Illustration: &Illustration{AssetID: image.AssetID, SHA256: image.SHA256, SizeBytes: image.SizeBytes}, RendererSHA256: RendererV11SHA256,
			})
			if err != nil {
				return err
			}
			candidate.Document = &document
		}
	}
	candidate.SourceImages = images
	return nil
}
