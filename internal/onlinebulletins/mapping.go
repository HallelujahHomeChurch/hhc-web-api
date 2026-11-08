package onlinebulletins

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrMapping = errors.New("invalid online sentence mapping")

type SentenceMapping struct {
	FromSentenceID string   `json:"fromSentenceId"`
	Status         string   `json:"status"`
	ToSentenceIDs  []string `json:"toSentenceIds"`
}

// PublicationMappings compares publication endpoints, not intermediate saves.
// Identity survives typo corrections and pagination. Structural replacements
// require explicit decisions; similar text is never authority to move notes.
func PublicationMappings(previous, next Document, decisions []SentenceMapping) ([]SentenceMapping, error) {
	if previous.IssueID != next.IssueID || previous.Series != next.Series || previous.ContentLocale != next.ContentLocale {
		return nil, ErrMapping
	}
	before, err := sentencePositions(previous)
	if err != nil {
		return nil, err
	}
	after, err := sentencePositions(next)
	if err != nil {
		return nil, err
	}
	if len(decisions) > len(before) {
		return nil, ErrMapping
	}
	hasNew := false
	for id := range after {
		if _, ok := before[id]; !ok {
			hasNew = true
		}
	}
	mappings := make(map[string]SentenceMapping, len(before))
	for id, position := range before {
		mapping := SentenceMapping{FromSentenceID: id, Status: "removed", ToSentenceIDs: []string{}}
		if target, exists := after[id]; exists {
			mapping.Status, mapping.ToSentenceIDs = "unchanged", []string{id}
			if target != position {
				mapping.Status = "moved"
			}
		} else if hasNew && !strings.HasPrefix(id, "canonical-") {
			mapping.Status = "requires_review"
		}
		mappings[id] = mapping
	}
	seen := map[string]bool{}
	for _, decision := range decisions {
		original, exists := mappings[decision.FromSentenceID]
		if !exists || seen[decision.FromSentenceID] || strings.HasPrefix(decision.FromSentenceID, "canonical-") || decision.ToSentenceIDs == nil || len(decision.ToSentenceIDs) > 1024 {
			return nil, ErrMapping
		}
		seen[decision.FromSentenceID] = true
		targets := map[string]bool{}
		for _, target := range decision.ToSentenceIDs {
			if _, exists := after[target]; !exists || targets[target] || strings.HasPrefix(target, "canonical-") {
				return nil, ErrMapping
			}
			targets[target] = true
		}
		switch decision.Status {
		case "removed":
			if _, retained := after[decision.FromSentenceID]; retained || len(targets) != 0 {
				return nil, ErrMapping
			}
		case "split":
			if len(targets) < 2 {
				return nil, ErrMapping
			}
		case "merged":
			if len(targets) != 1 {
				return nil, ErrMapping
			}
		case "unchanged", "moved":
			if decision.Status != original.Status || !slices.Equal(decision.ToSentenceIDs, original.ToSentenceIDs) {
				return nil, ErrMapping
			}
		default:
			return nil, ErrMapping
		}
		if _, retained := after[decision.FromSentenceID]; retained && !targets[decision.FromSentenceID] {
			return nil, ErrMapping
		}
		decision.ToSentenceIDs = slices.Clone(decision.ToSentenceIDs)
		if decision.ToSentenceIDs == nil {
			decision.ToSentenceIDs = []string{}
		}
		mappings[decision.FromSentenceID] = decision
	}
	// Sharing a target is legal only for a fully resolved merge. Neither a split
	// nor an automatic retained anchor may silently steal another sentence's note.
	owners := map[string][]string{}
	for id, mapping := range mappings {
		for _, target := range mapping.ToSentenceIDs {
			owners[target] = append(owners[target], id)
		}
	}
	for _, mapping := range mappings {
		for _, target := range mapping.ToSentenceIDs {
			if (mapping.Status == "merged") != (len(owners[target]) >= 2) {
				return nil, ErrMapping
			}
		}
	}
	ids := make([]string, 0, len(mappings))
	for id := range mappings {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	result := make([]SentenceMapping, 0, len(ids))
	for _, id := range ids {
		result = append(result, mappings[id])
	}
	return result, nil
}

func sentencePositions(document Document) (map[string]string, error) {
	if _, err := ValidateDocument(document); err != nil {
		return nil, ErrMapping
	}
	positions := map[string]string{}
	for i := range document.Components {
		component := &document.Components[i]
		for j, block := range componentBlocks(component) {
			for k, sentence := range block.Sentences {
				positions[sentence.ID] = fmt.Sprintf("%s/%s/%d/%d/%d", component.ID, block.ID, i, j, k)
			}
		}
	}
	for _, page := range document.LayoutManifest.Pages {
		for _, slot := range page.FixedSlots {
			switch slot.Element {
			case "title", "subtitle", "issueNumber", "date":
				positions["canonical-"+slot.Element] = slot.Element
			}
		}
	}
	return positions, nil
}
