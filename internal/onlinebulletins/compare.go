package onlinebulletins

import (
	"bytes"
	"encoding/json"
	"errors"
)

var ErrComparison = errors.New("invalid online comparison")

// Empty BlockID means a component structural change. An empty ComponentID is
// the component order. JSON values are server-owned read-only comparison data.
type ComparisonUnit struct {
	SourceRepairRequired bool            `json:"sourceRepairRequired,omitempty"`
	ComponentID          string          `json:"componentId"`
	BlockID              string          `json:"blockId"`
	Suggestion           string          `json:"suggestion"`
	Base                 json.RawMessage `json:"base"`
	Local                json.RawMessage `json:"local"`
	Incoming             json.RawMessage `json:"incoming"`
}

func CompareDocuments(base, local, incoming Document) ([]ComparisonUnit, error) {
	for _, document := range []Document{base, local, incoming} {
		if document.IssueID != base.IssueID || document.Series != base.Series || document.ContentLocale != base.ContentLocale {
			return nil, ErrComparison
		}
		if _, err := ValidateDocument(document); err != nil {
			return nil, ErrComparison
		}
	}
	lookup := func(document Document) (map[string]Component, []string) {
		components := map[string]Component{}
		order := []string{}
		for _, component := range document.Components {
			components[component.ID] = component
			order = append(order, component.ID)
		}
		return components, order
	}
	b, bo := lookup(base)
	l, lo := lookup(local)
	in, io := lookup(incoming)
	baseBlocks, localBlocks := map[string]Block{}, map[string]Block{}
	baseOwners := map[string]string{}
	for _, component := range base.Components {
		for _, block := range componentBlocks(&component) {
			baseBlocks[block.ID] = *block
			baseOwners[block.ID] = component.ID
		}
	}
	for _, component := range local.Components {
		for _, block := range componentBlocks(&component) {
			localBlocks[block.ID] = *block
		}
	}
	units := []ComparisonUnit{}
	if !bytes.Equal(comparisonJSON(bo), comparisonJSON(lo)) || !bytes.Equal(comparisonJSON(bo), comparisonJSON(io)) {
		units = append(units, ComparisonUnit{Suggestion: comparisonSuggestion(comparisonJSON(bo), comparisonJSON(lo), comparisonJSON(io)), Base: comparisonJSON(bo), Local: comparisonJSON(lo), Incoming: comparisonJSON(io)})
	}
	seen := map[string]bool{}
	for _, id := range append(append(append([]string{}, lo...), io...), bo...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		bc, bok := b[id]
		lc, lok := l[id]
		ic, iok := in[id]
		if !bok || !lok || !iok || !bytes.Equal(componentShape(bc), componentShape(lc)) || !bytes.Equal(componentShape(bc), componentShape(ic)) {
			unit := ComparisonUnit{ComponentID: id}
			if bok {
				unit.Base = comparisonJSON(bc)
			}
			if lok {
				unit.Local = comparisonJSON(lc)
			}
			if iok {
				unit.Incoming = comparisonJSON(ic)
			}
			unit.Suggestion = comparisonSuggestion(componentSemantic(bc, bok), componentSemantic(lc, lok), componentSemantic(ic, iok))
			if local.SourceAssetChecksum != incoming.SourceAssetChecksum && len(lc.Illustrations) > 0 {
				unit.Suggestion, unit.SourceRepairRequired = "conflict", true
			}
			// Structural changes can move authored text from another section.
			if iok {
				var preserved Component
				_ = json.Unmarshal(unit.Incoming, &preserved)
				changed := false
				for _, block := range componentBlocks(&preserved) {
					if baseOwners[block.ID] == id {
						continue
					}
					previous, existed := baseBlocks[block.ID]
					authored, retained := localBlocks[block.ID]
					if existed && retained && !bytes.Equal(comparisonJSON(comparisonBlock(previous)), comparisonJSON(comparisonBlock(authored))) {
						*block = authored
						changed = true
					}
				}
				if changed {
					// Only a new section needs a projected local alternative.
					if !bok && !lok {
						unit.Local = comparisonJSON(preserved)
					}
					unit.Suggestion = "conflict"
				}
			}
			units = append(units, unit)
			continue
		}
		bb, lb, ib := componentBlocks(&bc), componentBlocks(&lc), componentBlocks(&ic)
		for i, block := range bb {
			units = append(units, ComparisonUnit{ComponentID: id, BlockID: block.ID, Suggestion: comparisonSuggestion(comparisonJSON(comparisonBlock(*block)), comparisonJSON(comparisonBlock(*lb[i])), comparisonJSON(comparisonBlock(*ib[i]))), Base: comparisonJSON(block), Local: comparisonJSON(lb[i]), Incoming: comparisonJSON(ib[i])})
		}
	}
	return units, nil
}

func comparisonSuggestion(base, local, incoming []byte) string {
	switch {
	case bytes.Equal(local, incoming):
		return "same"
	case bytes.Equal(base, local):
		return "incoming"
	case bytes.Equal(base, incoming):
		return "local"
	default:
		return "conflict"
	}
}

func comparisonJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }

func componentShape(component Component) []byte {
	var copy Component
	_ = json.Unmarshal(comparisonJSON(component), &copy)
	for _, block := range componentBlocks(&copy) {
		*block = Block{ID: block.ID}
	}
	return comparisonJSON(copy)
}

func componentSemantic(component Component, present bool) []byte {
	if !present {
		return nil
	}
	var copy Component
	_ = json.Unmarshal(comparisonJSON(component), &copy)
	for _, block := range componentBlocks(&copy) {
		*block = comparisonBlock(*block)
	}
	return comparisonJSON(copy)
}

func comparisonBlock(block Block) Block {
	block.ID = ""
	block.Sentences = append([]Sentence{}, block.Sentences...)
	fontSize := block.Style.FontSize
	for i := range block.Sentences {
		block.Sentences[i].ID = ""
		block.Sentences[i].Source = nil
		for _, span := range block.Sentences[i].Spans {
			if span.FontSize != nil {
				fontSize = max(fontSize, *span.FontSize)
			}
		}
	}
	// These are the same minimum-spacing normalizations performed by the
	// compositor. Saving layout alone must not masquerade as an authored edit.
	block.Style.LineHeight = max(block.Style.LineHeight, 1.25*fontSize)
	block.Style.LetterSpacing = max(0, block.Style.LetterSpacing)
	return block
}
