package onlinebulletins

import (
	"bytes"
	"encoding/json"
	"io"
)

const MaxLearningRules = 4096

type LearningRuleInput struct {
	Signature string `json:"signature"`
	Version   int64  `json:"version"`
	Kind      string `json:"kind"`
}

type LearningApplication struct {
	ComponentID string `json:"componentId"`
	LearningRuleInput
}

func DecodeLearningRules(raw []byte) ([]LearningRuleInput, error) {
	if len(raw) == 0 {
		return nil, nil // Jobs queued before learning have no frozen rules.
	}
	if len(raw) > 1<<20 {
		return nil, ErrExtractionJob
	}
	var rules []LearningRuleInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&rules) != nil || rules == nil || !ValidLearningRules(rules) {
		return nil, ErrExtractionJob
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, ErrExtractionJob
	}
	return rules, nil
}

func ValidLearningRules(rules []LearningRuleInput) bool {
	if len(rules) > MaxLearningRules {
		return false
	}
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if !ValidLearningSignature(rule.Signature) || rule.Version < 1 || rule.Version > 9007199254740991 || seen[rule.Signature] {
			return false
		}
		switch rule.Kind {
		case "sermon", "testimony", "teaching", "reflection":
		default:
			return false
		}
		seen[rule.Signature] = true
	}
	return true
}

type LearningMetrics struct {
	BodySections    int `json:"bodySections"`
	SourceEligible  int `json:"sourceEligible"`
	UnknownEligible int `json:"unknownEligible"`
	Applied         int `json:"applied"`
	RuleCount       int `json:"ruleCount"`
}

func ApplyBodyKindRules(document *Document, evidence GeometryDocument, rules []LearningRuleInput) ([]LearningApplication, error) {
	applied, _, err := applyBodyKindRules(document, evidence, rules)
	return applied, err
}

func applyBodyKindRules(document *Document, evidence GeometryDocument, rules []LearningRuleInput) ([]LearningApplication, LearningMetrics, error) {
	metrics := LearningMetrics{RuleCount: len(rules)}
	if document != nil {
		for _, c := range document.Components {
			if c.Type == "bodySection" && c.BodySection != nil {
				metrics.BodySections++
			}
		}
	}
	if !ValidLearningRules(rules) {
		return nil, metrics, ErrExtractionJob
	}
	if document == nil || document.Series != "general" || document.ContentLocale != "zh-Hant" || (document.LayoutManifest.RendererVersion != "v8" && document.LayoutManifest.RendererVersion != "v9" && document.LayoutManifest.RendererVersion != "v10") || evidence.SourceSHA256 != document.SourceAssetChecksum || evidence.PopplerVersion != PopplerVersion {
		return nil, metrics, nil
	}
	bySignature := make(map[string]LearningRuleInput, len(rules))
	for _, rule := range rules {
		bySignature[rule.Signature] = rule
	}
	var applied []LearningApplication
	for _, component := range document.Components {
		if component.Type != "bodySection" || component.BodySection == nil {
			continue
		}
		correction, ok := sourceBodyKindCorrection(*document, component, evidence)
		if !ok {
			continue
		}
		metrics.SourceEligible++
		if component.BodySection.Kind != "unknown" {
			continue
		}
		metrics.UnknownEligible++
		if rule, found := bySignature[correction.Signature]; found {
			component.BodySection.Kind = rule.Kind
			applied = append(applied, LearningApplication{ComponentID: component.ID, LearningRuleInput: rule})
		}
	}
	metrics.Applied = len(applied)
	return applied, metrics, nil
}

func ValidLearningApplications(candidate DocumentCandidate, rules []LearningRuleInput) bool {
	if len(candidate.LearningApplications) == 0 {
		return true
	}
	document := candidate.Document
	if !ValidLearningRules(rules) || document == nil || len(candidate.LearningApplications) > len(document.Components) || document.ContentLocale != "zh-Hant" || document.Series != "general" || candidate.Evidence.SourceSHA256 != document.SourceAssetChecksum || candidate.Evidence.PopplerVersion != PopplerVersion {
		return false
	}
	allowed := make(map[string]LearningRuleInput, len(rules))
	for _, rule := range rules {
		allowed[rule.Signature] = rule
	}
	components := make(map[string]Component, len(document.Components))
	for _, component := range document.Components {
		components[component.ID] = component
	}
	seen := make(map[string]bool, len(candidate.LearningApplications))
	for _, application := range candidate.LearningApplications {
		component := components[application.ComponentID]
		if seen[application.ComponentID] || allowed[application.Signature] != application.LearningRuleInput || component.Type != "bodySection" || component.BodySection == nil || component.BodySection.Kind != application.Kind {
			return false
		}
		correction, ok := sourceBodyKindCorrection(*document, component, candidate.Evidence)
		if !ok || correction.Signature != application.Signature {
			return false
		}
		seen[application.ComponentID] = true
	}
	return true
}

type LearningRule struct {
	Signature       string `json:"signature"`
	Version         int64  `json:"version"`
	Status          string `json:"status"`
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	Speaker         string `json:"speaker"`
	ConfirmedIssues int    `json:"confirmedIssues"`
	Conflicted      bool   `json:"conflicted"`
}

type LearningRulePage struct {
	Items      []LearningRule `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type LearningRuleRevision struct {
	Version   int64  `json:"version"`
	Status    string `json:"status"`
	Kind      string `json:"kind"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
}

type LearningExample struct {
	SupersededByRevision *int64 `json:"supersededByRevision,omitempty"`
	IssueID              string `json:"issueId"`
	IssueNumber          int    `json:"issueNumber"`
	DocumentID           string `json:"documentId"`
	Revision             int64  `json:"revision"`
	ComponentID          string `json:"componentId"`
	SourcePage           int    `json:"sourcePage"`
	Kind                 string `json:"kind"`
	CreatedBy            string `json:"createdBy"`
	CreatedAt            string `json:"createdAt"`
}

type LearningRuleDetail struct {
	Rule      LearningRule           `json:"rule"`
	Revisions []LearningRuleRevision `json:"revisions"`
	Examples  []LearningExample      `json:"examples"`
}

func ValidLearningSignature(value string) bool { return hashPattern.MatchString(value) }
