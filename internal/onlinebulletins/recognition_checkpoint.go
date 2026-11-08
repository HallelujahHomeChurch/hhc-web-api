package onlinebulletins

import "time"

// A checkpoint is private job-owned evidence, never an API-supplied draft/proof.
type RecognitionCheckpoint struct {
	Canonical      CanonicalMetadata `json:"canonical"`
	Page           int               `json:"page"`
	Phase          string            `json:"phase"`
	InputSHA256    string            `json:"inputSha256"`
	ProtocolSHA256 string            `json:"protocolSha256"`
	Deployment     string            `json:"deployment"`
	ModelVersion   string            `json:"modelVersion"`
	Result         RecognitionResult `json:"result"`
	Usage          RecognitionUsage  `json:"usage"`
	TotalUsage     RecognitionUsage  `json:"totalUsage"`
	ErrorCode      string            `json:"errorCode,omitempty"`
}

const RecognitionJobTimeout = 10 * time.Minute

const RecognitionDeployment = "hhc-bulletin-ai"
const RecognitionModelName = "gpt-6.1-terra"
const RecognitionModelVersion = "2026-10-26"

func RecognitionExtractorVersion() string {
	return "azure-bulletin-v1-" + RecognitionProtocolSHA256()[:16]
}

type RecognitionPageEvidence struct {
	SemanticSHA256 string           `json:"semanticSha256"`
	Page           int              `json:"page"`
	InputSHA256    string           `json:"inputSha256"`
	Usage          RecognitionUsage `json:"usage"`
}

type RecognitionEvidence struct {
	ProtocolSHA256 string                    `json:"protocolSha256"`
	Deployment     string                    `json:"deployment"`
	ModelVersion   string                    `json:"modelVersion"`
	SourceSHA256   string                    `json:"sourceSha256"`
	Pages          []RecognitionPageEvidence `json:"pages"`
}

func ValidRecognitionEvidence(evidence *RecognitionEvidence, document Document) bool {
	if evidence == nil || evidence.ProtocolSHA256 != RecognitionProtocolSHA256() || evidence.Deployment != RecognitionDeployment || evidence.ModelVersion != RecognitionModelVersion || evidence.SourceSHA256 != document.SourceAssetChecksum || document.LayoutManifest.RendererVersion != "v11" || document.LayoutManifest.RendererArtifactSha256 != RendererV11SHA256 || len(evidence.Pages) != document.SourcePageCount {
		return false
	}
	for i, page := range evidence.Pages {
		if page.Page != i+1 || !hashPattern.MatchString(page.InputSHA256) || !hashPattern.MatchString(page.SemanticSHA256) || page.Usage.Requests < 2 || page.Usage.Model == "" || page.Usage.InputTokens < 0 || page.Usage.OutputTokens < 0 || page.Usage.ReasoningTokens < 0 || page.Usage.ReasoningTokens > page.Usage.OutputTokens {
			return false
		}
	}
	return true
}
