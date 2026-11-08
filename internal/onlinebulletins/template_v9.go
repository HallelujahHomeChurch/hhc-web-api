package onlinebulletins

func ApplyTemplateV9(source GeometryDocument, canonical CanonicalIssue, manifest LayoutManifest, snapshot TemplateSettings) (DocumentCandidate, []ReviewIssue, error) {
	text, valid := NormalizeTemplateText(snapshot.TemplateText)
	if manifest.RendererVersion != "v9" || snapshot.Version < 1 || snapshot.Version > 9007199254740991 || !valid || text != snapshot.TemplateText || canonical.Metadata.IssueNumber < 1739 {
		return DocumentCandidate{}, nil, ErrTemplateAssets
	}
	return applyTemplate(source, canonical, manifest, &snapshot)
}
