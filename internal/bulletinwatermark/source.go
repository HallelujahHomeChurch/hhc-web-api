package bulletinwatermark

func NormalizeSeries(series string) string {
	if series == "" {
		return "general"
	}
	return series
}

func ValidEdition(series, locale string) bool {
	series = NormalizeSeries(series)
	return series == "general" && (locale == "zh-Hant" || locale == "zh-Hans" || locale == "en") ||
		series == "children" && (locale == "zh-Hant" || locale == "en")
}

// Source binds a published projection to an asset owned by that bulletin version.
type Source struct {
	IssueID  string
	AssetID  string
	Series   string
	Locale   string
	Revision int64
	Filename string
}

// CandidateScope binds reconstruction to one immutable source and engine/model contract.
type CandidateScope struct {
	IssueID         string `json:"issueId"`
	Series          string `json:"series"`
	Locale          string `json:"locale"`
	Revision        int64  `json:"revision"`
	SourceAssetID   string `json:"sourceAssetId"`
	SourceSHA256    string `json:"sourceSha256"`
	RendererVersion string `json:"rendererVersion"`
	ModelSHA256     string `json:"modelSha256"`
}
