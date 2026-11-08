package onlinebulletins

// LayoutReviewError carries only locations from the exact input being composed.
// It never makes a failed layout publishable or exposes renderer error text.
type LayoutReviewError struct{ Issues []ReviewIssue }

func (*LayoutReviewError) Error() string { return ErrLayoutRunner.Error() }
func (*LayoutReviewError) Unwrap() error { return ErrLayoutRunner }

func layoutFailure(output []byte, document Document, contentHash string) error {
	var envelope struct {
		Failure struct {
			ContentHash            string `json:"contentHash"`
			RendererArtifactSHA256 string `json:"rendererArtifactSha256"`
			Locations              []struct {
				PageID string `json:"pageId"`
				SlotID string `json:"slotId"`
			} `json:"locations"`
		} `json:"failure"`
	}
	if len(output) > 32<<10 || decodeLayoutJSON(output, &envelope) != nil || envelope.Failure.ContentHash != contentHash || envelope.Failure.RendererArtifactSHA256 != document.LayoutManifest.RendererArtifactSha256 || len(envelope.Failure.Locations) == 0 || len(envelope.Failure.Locations) > 64 {
		return ErrLayoutRunner
	}
	issues := []ReviewIssue{}
	seen := map[ReviewIssue]bool{}
	for _, location := range envelope.Failure.Locations {
		if !idPattern.MatchString(location.PageID) || location.SlotID != "" && !idPattern.MatchString(location.SlotID) {
			return ErrLayoutRunner
		}
		found := false
		for index, page := range document.LayoutManifest.Pages {
			if page.PageID != location.PageID {
				continue
			}
			issue := ReviewIssue{Code: "layout_validation_failed", SourcePage: index + 1, Blocking: true}
			found = location.SlotID == ""
			for _, slot := range page.Slots {
				if slot.ID != location.SlotID {
					continue
				}
				issue.ComponentID, issue.BlockID = slot.ComponentID, slot.BlockID
				if len(slot.Fragments) > 0 {
					issue.SentenceID = slot.Fragments[0].SentenceID
				}
				found = true
			}
			for _, slot := range page.FixedSlots {
				if slot.ID == location.SlotID {
					issue.FixedElement = slot.Element
					found = true
				}
			}
			if found && !seen[issue] {
				issues = append(issues, issue)
				seen[issue] = true
			}
		}
		if !found {
			return ErrLayoutRunner
		}
	}
	return &LayoutReviewError{Issues: issues}
}
