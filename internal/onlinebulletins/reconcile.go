package onlinebulletins

import (
	"bytes"
	"encoding/json"
	"strings"
)

type identityCandidate struct{ id, group, text string }

type ExtractionIdentities struct{ Components, Blocks, Sentences map[string]string }

// ReconcileExtraction runs only against server-owned extraction revisions.
// Positional parser IDs are not identity: an insertion must not steal the next
// paragraph's notes. Unambiguous text matches and single replacements survive;
// ambiguous replacements get fresh IDs and require publication mapping review.
func ReconcileExtraction(base, incoming Document) (Document, *ExtractionIdentities, error) {
	if base.IssueID != incoming.IssueID || base.Series != incoming.Series || base.ContentLocale != incoming.ContentLocale {
		return Document{}, nil, ErrComparison
	}
	for _, document := range []Document{base, incoming} {
		if _, err := ValidateDocument(document); err != nil {
			return Document{}, nil, ErrComparison
		}
	}
	var result Document
	if json.Unmarshal(comparisonJSON(incoming), &result) != nil {
		return Document{}, nil, ErrComparison
	}
	componentCandidates := func(document Document) []identityCandidate {
		values := []identityCandidate{}
		for _, component := range document.Components {
			title := component.Type
			if component.BodySection != nil {
				title = blockIdentityText(component.BodySection.Title)
			}
			values = append(values, identityCandidate{component.ID, component.Type, title})
		}
		return values
	}
	componentIDs := identityPairs(componentCandidates(base), componentCandidates(incoming))
	// Reserve globally unique unchanged anchors before local typo matching.
	// A newly discovered heading can move a paragraph to another component.
	anchors := func(document Document) ([]identityCandidate, []identityCandidate) {
		blocks, sentences := []identityCandidate{}, []identityCandidate{}
		for _, component := range document.Components {
			for _, block := range componentBlocks(&component) {
				blocks = append(blocks, identityCandidate{block.ID, component.Type, blockIdentityText(*block)})
				for _, sentence := range block.Sentences {
					sentences = append(sentences, identityCandidate{sentence.ID, component.Type, layoutIdentityText(sentenceIdentityText(sentence))})
				}
			}
		}
		return blocks, sentences
	}
	baseBlocks, baseSentences := anchors(base)
	nextBlocks, nextSentences := anchors(incoming)
	globalBlocks := exactIdentityPairs(baseBlocks, nextBlocks)
	globalSentences := exactIdentityPairs(baseSentences, nextSentences)
	reservedBlocks, reservedSentences := map[string]bool{}, map[string]bool{}
	for _, id := range globalBlocks {
		reservedBlocks[id] = true
	}
	for _, id := range globalSentences {
		reservedSentences[id] = true
	}
	baseComponents := map[string]Component{}
	oldBlocks := map[string]Block{}
	for _, component := range base.Components {
		baseComponents[component.ID] = component
		for _, block := range componentBlocks(&component) {
			oldBlocks[block.ID] = *block
		}
	}
	blockIDs, sentenceIDs := map[string]string{}, map[string]string{}
	fresh := func(kind, id string) string { return templateID("n-", incoming.SourceAssetChecksum+":"+kind+":"+id) }
	for i := range result.Components {
		component := &result.Components[i]
		originalID := component.ID
		paired, matched := componentIDs[originalID]
		if !matched {
			paired = fresh("component", originalID)
			componentIDs[originalID] = paired
		}
		old := baseComponents[paired]
		oldSequence, newSequence := []identityCandidate{}, []identityCandidate{}
		for _, block := range componentBlocks(&old) {
			for _, sentence := range block.Sentences {
				oldSequence = append(oldSequence, identityCandidate{sentence.ID, "sentence", layoutIdentityText(sentenceIdentityText(sentence))})
			}
		}
		for _, block := range componentBlocks(component) {
			for _, sentence := range block.Sentences {
				newSequence = append(newSequence, identityCandidate{sentence.ID, "sentence", layoutIdentityText(sentenceIdentityText(sentence))})
			}
		}
		if pairs, unchanged := orderedIdentityPairs(oldSequence, newSequence); matched && unchanged {
			for nextID, oldID := range pairs {
				if existing, found := globalSentences[nextID]; found && existing == oldID || !found && !reservedSentences[oldID] {
					globalSentences[nextID], reservedSentences[oldID] = oldID, true
				}
			}
		}
		before, after := []identityCandidate{}, []identityCandidate{}
		for _, block := range componentBlocks(&old) {
			if !reservedBlocks[block.ID] {
				before = append(before, identityCandidate{block.ID, "block", blockIdentityText(*block)})
			}
		}
		for _, block := range componentBlocks(component) {
			if _, matched := globalBlocks[block.ID]; !matched {
				after = append(after, identityCandidate{block.ID, "block", blockIdentityText(*block)})
			}
		}
		pairs := identityPairs(before, after)
		for _, block := range componentBlocks(component) {
			oldBlockID := block.ID
			newID, ok := globalBlocks[oldBlockID]
			if !ok {
				newID, ok = pairs[oldBlockID]
			}
			if !ok {
				newID = fresh("block", oldBlockID)
			}
			blockIDs[oldBlockID] = newID
			oldBlock := oldBlocks[newID]
			before, after = []identityCandidate{}, []identityCandidate{}
			localOld, localNew := map[string]bool{}, map[string]bool{}
			for _, sentence := range oldBlock.Sentences {
				localOld[sentence.ID] = true
			}
			for _, sentence := range block.Sentences {
				localNew[globalSentences[sentence.ID]] = true
			}
			for _, sentence := range oldBlock.Sentences {
				if !reservedSentences[sentence.ID] || localNew[sentence.ID] {
					before = append(before, identityCandidate{sentence.ID, "sentence", layoutIdentityText(sentenceIdentityText(sentence))})
				}
			}
			for _, sentence := range block.Sentences {
				if id, matched := globalSentences[sentence.ID]; !matched || localOld[id] {
					after = append(after, identityCandidate{sentence.ID, "sentence", layoutIdentityText(sentenceIdentityText(sentence))})
				}
			}
			spairs := identityPairs(before, after)
			for j := range block.Sentences {
				sentence := &block.Sentences[j]
				newSentenceID, ok := globalSentences[sentence.ID]
				if !ok {
					newSentenceID, ok = spairs[sentence.ID]
				}
				if !ok {
					newSentenceID = fresh("sentence", sentence.ID)
				}
				sentenceIDs[sentence.ID] = newSentenceID
				sentence.ID = newSentenceID
			}
			block.ID = newID
		}
		component.ID = paired
		// Reclassification alone must not create work for unchanged source sections.
		if matched && old.BodySection != nil && component.BodySection != nil && old.BodySection.Kind != "unknown" && component.BodySection.Kind != "unknown" {
			kind := component.BodySection.Kind
			component.BodySection.Kind = old.BodySection.Kind
			if !bytes.Equal(componentSemantic(old, true), componentSemantic(*component, true)) {
				component.BodySection.Kind = kind
			}
		}
	}
	for i := range result.LayoutManifest.Pages {
		for j := range result.LayoutManifest.Pages[i].FixedSlots {
			slot := &result.LayoutManifest.Pages[i].FixedSlots[j]
			if slot.Element != "speakerSeparator" {
				continue
			}
			for original, stable := range componentIDs {
				if slot.ID == "fixed-"+original+"-speakerSeparator" {
					slot.ID = "fixed-" + stable + "-speakerSeparator"
					break
				}
			}
		}
		for j := range result.LayoutManifest.Pages[i].Slots {
			slot := &result.LayoutManifest.Pages[i].Slots[j]
			slot.ComponentID = componentIDs[slot.ComponentID]
			slot.BlockID = blockIDs[slot.BlockID]
			for k := range slot.Fragments {
				slot.Fragments[k].SentenceID = sentenceIDs[slot.Fragments[k].SentenceID]
			}
		}
	}
	result.LayoutManifest.ContentHash, result.LayoutManifest.LayoutValidationHash = "", ""
	if _, err := ValidateDocument(result); err != nil {
		return Document{}, nil, ErrComparison
	}
	return result, &ExtractionIdentities{Components: componentIDs, Blocks: blockIDs, Sentences: sentenceIDs}, nil
}

func sentenceIdentityText(sentence Sentence) string {
	var text strings.Builder
	for _, span := range sentence.Spans {
		text.WriteString(span.Text)
	}
	return text.String()
}
func blockIdentityText(block Block) string {
	texts := []string{}
	for _, sentence := range block.Sentences {
		texts = append(texts, layoutIdentityText(sentenceIdentityText(sentence)))
	}
	return string(comparisonJSON(texts))
}

func exactIdentityPairs(before, after []identityCandidate) map[string]string {
	result := map[string]string{}
	oldByText, newByText := map[string][]identityCandidate{}, map[string][]identityCandidate{}
	for _, value := range before {
		key := value.group + "\x00" + value.text
		oldByText[key] = append(oldByText[key], value)
	}
	for _, value := range after {
		key := value.group + "\x00" + value.text
		newByText[key] = append(newByText[key], value)
	}
	for key, values := range newByText {
		if len(values) == 1 && len(oldByText[key]) == 1 {
			result[values[0].id] = oldByText[key][0].id
		}
	}
	return result
}

func orderedIdentityPairs(before, after []identityCandidate) (map[string]string, bool) {
	// Within one matched parent, an unchanged ordered sequence proves duplicate
	// line identities. Never pair duplicate text globally or after an insertion.
	if len(before) == len(after) {
		same := true
		for i := range before {
			same = same && before[i].group == after[i].group && before[i].text == after[i].text
		}
		if same {
			pairs := map[string]string{}
			for i := range before {
				pairs[after[i].id] = before[i].id
			}
			return pairs, true
		}
	}
	return nil, false
}

func identityPairs(before, after []identityCandidate) map[string]string {
	if pairs, unchanged := orderedIdentityPairs(before, after); unchanged {
		return pairs
	}
	result, used := exactIdentityPairs(before, after), map[string]bool{}
	for _, id := range result {
		used[id] = true
	}
	oldRemaining, newRemaining := map[string][]identityCandidate{}, map[string][]identityCandidate{}
	for _, value := range before {
		if !used[value.id] {
			oldRemaining[value.group] = append(oldRemaining[value.group], value)
		}
	}
	for _, value := range after {
		if _, ok := result[value.id]; !ok {
			newRemaining[value.group] = append(newRemaining[value.group], value)
		}
	}
	for group, values := range newRemaining {
		if len(values) == 1 && len(oldRemaining[group]) == 1 {
			result[values[0].id] = oldRemaining[group][0].id
			used[oldRemaining[group][0].id] = true
		}
	}
	// Exact surrounding anchors bound independent single replacements, even when
	// another gap in the same paragraph contains a real split or insertion.
	oldIndex := map[string]int{}
	for i, value := range before {
		oldIndex[value.id] = i
	}
	last := -1
	for _, value := range after {
		if id, matched := result[value.id]; matched {
			if oldIndex[id] <= last {
				return result
			}
			last = oldIndex[id]
		}
	}
	previousOld, previousNew := -1, -1
	for j := 0; j <= len(after); j++ {
		i := len(before)
		if j < len(after) {
			id, matched := result[after[j].id]
			if !matched {
				continue
			}
			i = oldIndex[id]
		}
		if i-previousOld == 2 && j-previousNew == 2 {
			old, next := before[i-1], after[j-1]
			if old.group == next.group && !used[old.id] && result[next.id] == "" {
				result[next.id], used[old.id] = old.id, true
			}
		}
		previousOld, previousNew = i, j
	}
	return result
}

// RestorePublishedSentenceIdentities repairs draft-only parser churn. Only
// server-extracted, unoccupied identities proven against published content are reused;
// new component structure, typography, source geometry and text stay authored.
func RestorePublishedSentenceIdentities(published, extractedSource, draft Document) (Document, map[string]string, error) {
	_, identities, err := ReconcileExtraction(published, draft)
	if err != nil {
		return Document{}, nil, err
	}
	var result Document
	if json.Unmarshal(comparisonJSON(draft), &result) != nil {
		return Document{}, nil, ErrComparison
	}
	previous, occupied := map[string]bool{}, map[string]bool{}
	eligible := map[string]bool{}
	for _, component := range extractedSource.Components {
		for _, block := range componentBlocks(&component) {
			for _, sentence := range block.Sentences {
				eligible[sentence.ID] = true
			}
		}
	}
	for _, component := range published.Components {
		for _, block := range componentBlocks(&component) {
			for _, sentence := range block.Sentences {
				previous[sentence.ID] = true
			}
		}
	}
	for _, component := range draft.Components {
		for _, block := range componentBlocks(&component) {
			for _, sentence := range block.Sentences {
				occupied[sentence.ID] = true
			}
		}
	}
	remap := map[string]string{}
	for i := range result.Components {
		for _, block := range componentBlocks(&result.Components[i]) {
			for j := range block.Sentences {
				sentence := &block.Sentences[j]
				id := identities.Sentences[sentence.ID]
				if eligible[sentence.ID] && !previous[sentence.ID] && previous[id] && !occupied[id] {
					remap[sentence.ID], occupied[id] = id, true
					sentence.ID = id
				}
			}
		}
	}
	for i := range result.LayoutManifest.Pages {
		for j := range result.LayoutManifest.Pages[i].Slots {
			for k := range result.LayoutManifest.Pages[i].Slots[j].Fragments {
				fragment := &result.LayoutManifest.Pages[i].Slots[j].Fragments[k]
				if id := remap[fragment.SentenceID]; id != "" {
					fragment.SentenceID = id
				}
			}
		}
	}
	result.LayoutManifest.ContentHash, result.LayoutManifest.LayoutValidationHash = "", ""
	if _, err := ValidateDocument(result); err != nil {
		return Document{}, nil, err
	}
	return result, remap, nil
}
