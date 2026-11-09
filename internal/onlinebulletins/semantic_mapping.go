package onlinebulletins

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

var ErrSemanticMapping = errors.New("invalid_semantic_mapping")

// Decisions refer to source-owned sentence IDs; they never carry replacement text.
type SemanticMappingDecision struct {
	FromSentenceID string   `json:"fromSentenceId"`
	Kind           string   `json:"kind"`
	ToSentenceIDs  []string `json:"toSentenceIds"`
}

// ApplySemanticMappings retains equivalent identities before fresh layout proof.
// Uncertain old anchors retain history rather than attach notes to changed words.
func ApplySemanticMappings(previous, next Document, decisions []SemanticMappingDecision) (Document, []SentenceMapping, error) {
	if previous.IssueID != next.IssueID || previous.Series != next.Series || previous.ContentLocale != next.ContentLocale {
		return Document{}, nil, ErrSemanticMapping
	}
	for _, d := range []Document{previous, next} {
		if _, err := ValidateDocument(d); err != nil {
			return Document{}, nil, ErrSemanticMapping
		}
	}
	index := func(d Document) map[string]Sentence {
		out := map[string]Sentence{}
		for _, c := range d.Components {
			for _, b := range componentBlocks(&c) {
				for _, s := range b.Sentences {
					out[s.ID] = s
				}
			}
		}
		return out
	}
	before, after := index(previous), index(next)
	if len(decisions) > len(before) {
		return Document{}, nil, ErrSemanticMapping
	}
	chosen := map[string]SemanticMappingDecision{}
	equivalents := map[string]string{}
	for _, d := range decisions {
		if _, ok := before[d.FromSentenceID]; !ok || strings.HasPrefix(d.FromSentenceID, "canonical-") {
			return Document{}, nil, ErrSemanticMapping
		}
		if _, duplicate := chosen[d.FromSentenceID]; duplicate {
			return Document{}, nil, ErrSemanticMapping
		}
		targets := map[string]bool{}
		for _, id := range d.ToSentenceIDs {
			if _, ok := after[id]; !ok || targets[id] || strings.HasPrefix(id, "canonical-") {
				return Document{}, nil, ErrSemanticMapping
			}
			targets[id] = true
		}
		switch d.Kind {
		case "equivalent":
			if len(d.ToSentenceIDs) != 1 {
				return Document{}, nil, ErrSemanticMapping
			}
			target := d.ToSentenceIDs[0]
			if target != d.FromSentenceID {
				if _, occupied := after[d.FromSentenceID]; occupied {
					return Document{}, nil, ErrSemanticMapping
				}
				if _, retained := before[target]; retained {
					return Document{}, nil, ErrSemanticMapping
				}
			}
			if _, used := equivalents[target]; used {
				return Document{}, nil, ErrSemanticMapping
			}
			equivalents[target] = d.FromSentenceID
		case "historical":
			if len(targets) != 0 {
				return Document{}, nil, ErrSemanticMapping
			}
		case "split":
			if len(targets) < 2 {
				return Document{}, nil, ErrSemanticMapping
			}
		case "merged":
			if len(targets) != 1 {
				return Document{}, nil, ErrSemanticMapping
			}
		default:
			return Document{}, nil, ErrSemanticMapping
		}
		chosen[d.FromSentenceID] = d
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return Document{}, nil, ErrSemanticMapping
	}
	var result Document
	if json.Unmarshal(raw, &result) != nil {
		return Document{}, nil, ErrSemanticMapping
	}
	remap := map[string]string{}
	explicit := []SentenceMapping{}
	for id, old := range before {
		decision, selected := chosen[id]
		current, retained := after[id]
		if !selected {
			if retained && sentenceIdentityText(old) == sentenceIdentityText(current) {
				continue
			}
			decision = SemanticMappingDecision{FromSentenceID: id, Kind: "historical"}
		}
		if decision.Kind == "equivalent" {
			if target := decision.ToSentenceIDs[0]; target != id {
				remap[target] = id
			}
			continue
		}
		if retained && !slices.Contains(decision.ToSentenceIDs, id) {
			// Free a reused parser ID without changing the new source's text or geometry.
			fresh := templateID("n-", next.SourceAssetChecksum+":semantic:"+id+":"+sentenceIdentityText(current))
			if _, occupied := after[fresh]; occupied {
				return Document{}, nil, ErrSemanticMapping
			}
			remap[id] = fresh
		}
		status := decision.Kind
		if status == "historical" {
			status = "removed"
		}
		explicit = append(explicit, SentenceMapping{FromSentenceID: id, Status: status, ToSentenceIDs: append([]string{}, decision.ToSentenceIDs...)})
	}
	for i := range result.Components {
		for _, b := range componentBlocks(&result.Components[i]) {
			for j := range b.Sentences {
				if id, ok := remap[b.Sentences[j].ID]; ok {
					b.Sentences[j].ID = id
				}
			}
		}
	}
	for i := range result.LayoutManifest.Pages {
		for j := range result.LayoutManifest.Pages[i].Slots {
			for k := range result.LayoutManifest.Pages[i].Slots[j].Fragments {
				f := &result.LayoutManifest.Pages[i].Slots[j].Fragments[k]
				if id, ok := remap[f.SentenceID]; ok {
					f.SentenceID = id
				}
			}
		}
	}
	for i := range explicit {
		for j, id := range explicit[i].ToSentenceIDs {
			if replacement, ok := remap[id]; ok {
				explicit[i].ToSentenceIDs[j] = replacement
			}
		}
	}
	result.LayoutManifest.ContentHash, result.LayoutManifest.LayoutValidationHash = "", ""
	if _, err = ValidateDocument(result); err != nil {
		return Document{}, nil, ErrSemanticMapping
	}
	mappings, err := PublicationMappings(previous, result, explicit)
	if err != nil {
		return Document{}, nil, ErrSemanticMapping
	}
	for _, m := range mappings {
		if m.Status == "requires_review" {
			return Document{}, nil, ErrSemanticMapping
		}
	}
	return result, mappings, nil
}
