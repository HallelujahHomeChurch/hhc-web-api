package onlinebulletins

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
)

type ComparisonChoice struct {
	ComponentID string          `json:"componentId"`
	BlockID     string          `json:"blockId"`
	Choice      string          `json:"choice"`
	Value       json.RawMessage `json:"value,omitempty"`
}

func MergeDocuments(base, local, incoming Document, choices []ComparisonChoice) (Document, error) {
	units, err := CompareDocuments(base, local, incoming)
	if err != nil || len(choices) > len(units) {
		return Document{}, ErrComparison
	}
	decisions := map[string]ComparisonChoice{}
	for _, choice := range choices {
		key := choice.ComponentID + "/" + choice.BlockID
		if _, exists := decisions[key]; exists {
			return Document{}, ErrComparison
		}
		if choice.Choice != "local" && choice.Choice != "incoming" && choice.Choice != "manual" {
			return Document{}, ErrComparison
		}
		if (choice.Choice == "manual") != (len(choice.Value) > 0) {
			return Document{}, ErrComparison
		}
		decisions[key] = choice
	}
	var seed Document
	if json.Unmarshal(comparisonJSON(local), &seed) != nil {
		return Document{}, ErrComparison
	}
	components := map[string]Component{}
	order := []string{}
	var editable []Component
	if json.Unmarshal(comparisonJSON(local.Components), &editable) != nil {
		return Document{}, ErrComparison
	}
	for _, component := range editable {
		components[component.ID] = component
		order = append(order, component.ID)
	}
	for _, unit := range units {
		key := unit.ComponentID + "/" + unit.BlockID
		choice, explicit := decisions[key]
		delete(decisions, key)
		if !explicit {
			choice.Choice = unit.Suggestion
		}
		var value json.RawMessage
		switch choice.Choice {
		case "local":
			value = unit.Local
		case "incoming", "same":
			// Identical words still need the current extraction's source anchors.
			value = unit.Incoming
		case "manual":
			value = choice.Value
		default:
			return Document{}, ErrComparison
		}
		if unit.ComponentID == "" {
			if strictComparisonValue(value, &order) != nil {
				return Document{}, ErrComparison
			}
			continue
		}
		if unit.BlockID == "" {
			if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				delete(components, unit.ComponentID)
				continue
			}
			var component Component
			if strictComparisonValue(value, &component) != nil || component.ID != unit.ComponentID {
				return Document{}, ErrComparison
			}
			if choice.Choice == "local" && unit.SourceRepairRequired {
				return Document{}, ErrComparison
			}
			if choice.Choice == "manual" {
				if len(component.Illustrations) != 0 {
					return Document{}, ErrComparison
				}
				if !unit.SourceRepairRequired {
					component.Illustrations = components[component.ID].Illustrations
				}
				for _, block := range componentBlocks(&component) {
					if hasSourceCoordinates(*block) {
						return Document{}, ErrComparison
					}
				}
			}
			if choice.Choice == "incoming" {
				var old Component
				if len(unit.Base) > 0 && json.Unmarshal(unit.Base, &old) == nil {
					blocks := map[string]Block{}
					for _, block := range componentBlocks(&old) {
						blocks[block.ID] = *block
					}
					for _, block := range componentBlocks(&component) {
						if previous, ok := blocks[block.ID]; ok {
							retainSentenceIDs(previous, block)
						}
					}
				}
			}
			components[component.ID] = component
			continue
		}
		var block Block
		if strictComparisonValue(value, &block) != nil || block.ID != unit.BlockID {
			return Document{}, ErrComparison
		}
		if choice.Choice == "manual" && hasSourceCoordinates(block) {
			return Document{}, ErrComparison
		}
		if choice.Choice == "incoming" {
			var old Block
			if json.Unmarshal(unit.Base, &old) != nil {
				return Document{}, ErrComparison
			}
			retainSentenceIDs(old, &block)
		}
		component := components[unit.ComponentID]
		found := false
		for _, target := range componentBlocks(&component) {
			if target.ID == block.ID {
				*target = block
				found = true
				break
			}
		}
		if !found {
			return Document{}, ErrComparison
		}
		components[component.ID] = component
	}
	if len(decisions) != 0 {
		return Document{}, ErrComparison
	}
	selected := make([]Component, 0, len(order))
	for _, id := range order {
		component, exists := components[id]
		if !exists {
			return Document{}, ErrComparison
		}
		delete(components, id)
		selected = append(selected, component)
	}
	if len(components) != 0 {
		return Document{}, ErrComparison
	}
	// Prefer the new server extraction's geometry when it covers every chosen
	// component. This includes genuinely new sections, not browser rectangles.
	if IncomingGeometryCovers(incoming, selected) {
		seed = Document{}
		if json.Unmarshal(comparisonJSON(incoming), &seed) != nil {
			return Document{}, ErrComparison
		}
	} else if mixed, ok := sameSourceMergeGeometry(local, incoming, selected); ok {
		seed = mixed
	}
	// The new Base is the incoming source. Mixed authored text has no invented
	// source coordinates; immutable predecessor revisions retain old provenance.
	seed.SourceAssetChecksum, seed.SourcePageCount = incoming.SourceAssetChecksum, incoming.SourcePageCount
	pictures := map[string][]Illustration{}
	for _, component := range selected {
		pictures[component.ID] = component.Illustrations
		if len(component.Illustrations) > 0 {
			seed.LayoutManifest.RendererVersion, seed.LayoutManifest.RendererArtifactSha256 = "v11", RendererV11SHA256
		}
	}
	for i := range seed.Components {
		seed.Components[i].Illustrations = pictures[seed.Components[i].ID]
		for _, block := range componentBlocks(&seed.Components[i]) {
			for j := range block.Sentences {
				block.Sentences[j].Source = nil
			}
		}
	}
	for i := range selected {
		selected[i].Illustrations = nil
		for _, block := range componentBlocks(&selected[i]) {
			for j := range block.Sentences {
				block.Sentences[j].Source = nil
			}
		}
	}
	draft, err := EditComponents(seed, selected)
	if err != nil {
		return Document{}, ErrComparison
	}
	return draft, nil
}

// Same-source authored paragraphs can keep their own page fragments while
// unchanged paragraphs and fixed template elements use the new extraction.
func sameSourceMergeGeometry(local, incoming Document, selected []Component) (Document, bool) {
	if local.SourceAssetChecksum != incoming.SourceAssetChecksum || !slices.Equal(local.Pages, incoming.Pages) {
		return Document{}, false
	}
	var seed Document
	if json.Unmarshal(comparisonJSON(incoming), &seed) != nil {
		return Document{}, false
	}
	type ownedBlock struct {
		component string
		block     Block
	}
	old := map[string]ownedBlock{}
	for _, component := range local.Components {
		for _, block := range componentBlocks(&component) {
			old[block.ID] = ownedBlock{component.ID, *block}
		}
	}
	retained := map[string]bool{}
	for _, component := range selected {
		var source *Component
		for i := range seed.Components {
			if seed.Components[i].ID == component.ID {
				source = &seed.Components[i]
				break
			}
		}
		if source == nil {
			return Document{}, false
		}
		for _, block := range componentBlocks(&component) {
			var target *Block
			for _, candidate := range componentBlocks(source) {
				if candidate.ID == block.ID {
					target = candidate
					break
				}
			}
			if target == nil {
				return Document{}, false
			}
			ids := map[string]bool{}
			for _, sentence := range target.Sentences {
				ids[sentence.ID] = true
			}
			covered := true
			for _, sentence := range block.Sentences {
				covered = covered && ids[sentence.ID]
			}
			if covered {
				continue
			}
			previous, exists := old[block.ID]
			if !exists || previous.component != component.ID || len(previous.block.Sentences) != len(block.Sentences) {
				return Document{}, false
			}
			for i, sentence := range block.Sentences {
				if sentence.ID != previous.block.Sentences[i].ID {
					return Document{}, false
				}
			}
			if json.Unmarshal(comparisonJSON(previous.block), target) != nil {
				return Document{}, false
			}
			retained[block.ID] = true
		}
	}
	for i := range seed.LayoutManifest.Pages {
		page := &seed.LayoutManifest.Pages[i]
		slots := make([]Slot, 0, len(page.Slots))
		for _, slot := range page.Slots {
			if !retained[slot.BlockID] {
				slots = append(slots, slot)
			}
		}
		for _, slot := range local.LayoutManifest.Pages[i].Slots {
			if retained[slot.BlockID] {
				slots = append(slots, slot)
			}
		}
		page.Slots = slots
	}
	if _, err := ValidateDocument(seed); err != nil || !IncomingGeometryCovers(seed, selected) {
		return Document{}, false
	}
	return seed, true
}

// IncomingGeometryCovers is the merge's source-evidence selection rule. Review
// findings may be refreshed only when the incoming geometry covers the chosen
// anchors in the same component/block ownership, including paragraph regrouping.
func IncomingGeometryCovers(incoming Document, selected []Component) bool {
	ids := make(map[string]bool, len(incoming.Components))
	blockOwners, sentenceBlocks := map[string]string{}, map[string]string{}
	for _, component := range incoming.Components {
		ids[component.ID] = true
		for _, block := range componentBlocks(&component) {
			blockOwners[block.ID] = component.ID
			for _, sentence := range block.Sentences {
				sentenceBlocks[sentence.ID] = block.ID
			}
		}
	}
	for _, component := range selected {
		if !ids[component.ID] {
			return false
		}
		for _, block := range componentBlocks(&component) {
			if blockOwners[block.ID] != component.ID {
				return false
			}
			for _, sentence := range block.Sentences {
				if sentenceBlocks[sentence.ID] != block.ID {
					return false
				}
			}
		}
	}
	return true
}

func hasSourceCoordinates(block Block) bool {
	for _, sentence := range block.Sentences {
		if sentence.Source != nil {
			return true
		}
	}
	return false
}

func strictComparisonValue(data []byte, target any) error {
	if len(data) > MaxDocumentBytes {
		return ErrComparison
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrComparison
	}
	return nil
}

// Exact IDs and unique identical text survive re-extraction. A single remaining
// replacement is a typo correction. Multiple ambiguous replacements keep new
// identities and must be resolved in the publication mapping review.
func retainSentenceIDs(base Block, incoming *Block) {
	text := func(sentence Sentence) string {
		var out strings.Builder
		for _, span := range sentence.Spans {
			out.WriteString(span.Text)
		}
		return out.String()
	}
	old := map[string]Sentence{}
	oldText := map[string][]string{}
	newText := map[string]int{}
	for _, sentence := range base.Sentences {
		old[sentence.ID] = sentence
		oldText[text(sentence)] = append(oldText[text(sentence)], sentence.ID)
	}
	for _, sentence := range incoming.Sentences {
		newText[text(sentence)]++
	}
	matched := map[int]bool{}
	used := map[string]bool{}
	for i, sentence := range incoming.Sentences {
		if _, ok := old[sentence.ID]; ok {
			used[sentence.ID] = true
			matched[i] = true
		}
	}
	for i, sentence := range incoming.Sentences {
		candidates := oldText[text(sentence)]
		if !matched[i] && len(candidates) == 1 && newText[text(sentence)] == 1 && !used[candidates[0]] {
			incoming.Sentences[i].ID = candidates[0]
			used[candidates[0]] = true
			matched[i] = true
		}
	}
	remainingOld := ""
	oldCount, newCount, newIndex := 0, 0, 0
	for id := range old {
		if !used[id] {
			oldCount++
			remainingOld = id
		}
	}
	for i := range incoming.Sentences {
		if !matched[i] {
			newCount++
			newIndex = i
		}
	}
	if oldCount == 1 && newCount == 1 {
		incoming.Sentences[newIndex].ID = remainingOld
	}
}
