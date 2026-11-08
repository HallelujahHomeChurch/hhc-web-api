package onlinebulletins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"
)

const SimplifiedConverterVersion = "opencc-js-1.4.1-tw2s-1"

var ErrSimplifiedConversion = errors.New("simplified_conversion_failed")

// Paths are fixed by the extractor image, never supplied by a request.
type SimplifiedRunner struct{ NodePath, ScriptPath string }

type conversionText struct {
	Text       string `json:"text"`
	Boundaries []int  `json:"boundaries"`
}

func (r SimplifiedRunner) ConvertMetadata(ctx context.Context, source CanonicalMetadata) (CanonicalMetadata, error) {
	inputs := []conversionText{}
	for _, text := range []string{source.Title, source.Subtitle} {
		boundaries := []int{0}
		if text != "" {
			boundaries = append(boundaries, utf8.RuneCountInString(text))
		}
		inputs = append(inputs, conversionText{Text: text, Boundaries: boundaries})
	}
	outputs, err := r.convert(ctx, inputs)
	if err != nil {
		return CanonicalMetadata{}, err
	}
	source.Title, source.Subtitle = outputs[0].Text, outputs[1].Text
	return source, nil
}

func (r SimplifiedRunner) ConvertSimplified(ctx context.Context, source Document) (Document, error) {
	if source.ContentLocale != "zh-Hant" {
		return Document{}, ErrSimplifiedConversion
	}
	if _, err := ValidateDocument(source); err != nil {
		return Document{}, ErrSimplifiedConversion
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return Document{}, ErrSimplifiedConversion
	}
	var draft Document
	if json.Unmarshal(raw, &draft) != nil {
		return Document{}, ErrSimplifiedConversion
	}
	var sentences []*Sentence
	var inputs []conversionText
	indexes := map[string]int{}
	for i := range draft.Components {
		for _, block := range componentBlocks(&draft.Components[i]) {
			for j := range block.Sentences {
				sentence := &block.Sentences[j]
				input := conversionText{Boundaries: []int{0}}
				length := 0
				for _, span := range sentence.Spans {
					input.Text += span.Text
					length += utf8.RuneCountInString(span.Text)
					input.Boundaries = append(input.Boundaries, length)
				}
				indexes[sentence.ID] = len(inputs)
				sentences = append(sentences, sentence)
				inputs = append(inputs, input)
			}
		}
	}
	for _, page := range draft.LayoutManifest.Pages {
		for _, slot := range page.Slots {
			for _, fragment := range slot.Fragments {
				index := indexes[fragment.SentenceID]
				inputs[index].Boundaries = append(inputs[index].Boundaries, fragment.Start, fragment.End)
			}
		}
	}
	for i := range inputs {
		slices.Sort(inputs[i].Boundaries)
		inputs[i].Boundaries = slices.Compact(inputs[i].Boundaries)
	}
	if draft.TemplateSnapshot != nil {
		for _, text := range []string{draft.TemplateSnapshot.VisionMission, draft.TemplateSnapshot.VisionFellowship, draft.TemplateSnapshot.VisionCommitment} {
			inputs = append(inputs, conversionText{Text: text, Boundaries: []int{0, utf8.RuneCountInString(text)}})
		}
	}
	outputs, err := r.convert(ctx, inputs)
	if err != nil {
		return Document{}, err
	}
	maps := make([]map[int]int, len(inputs))
	for i, output := range outputs[:len(sentences)] {
		maps[i] = map[int]int{}
		for j, old := range inputs[i].Boundaries {
			maps[i][old] = output.Boundaries[j]
		}
		chars := []rune(output.Text)
		start := 0
		for j := range sentences[i].Spans {
			span := &sentences[i].Spans[j]
			end := start + utf8.RuneCountInString(span.Text)
			span.Text = string(chars[maps[i][start]:maps[i][end]])
			start = end
		}
	}
	if draft.TemplateSnapshot != nil {
		draft.TemplateSnapshot.VisionMission = outputs[len(sentences)].Text
		draft.TemplateSnapshot.VisionFellowship = outputs[len(sentences)+1].Text
		draft.TemplateSnapshot.VisionCommitment = outputs[len(sentences)+2].Text
	}
	for i := range draft.LayoutManifest.Pages {
		for j := range draft.LayoutManifest.Pages[i].Slots {
			for k := range draft.LayoutManifest.Pages[i].Slots[j].Fragments {
				fragment := &draft.LayoutManifest.Pages[i].Slots[j].Fragments[k]
				boundaries := maps[indexes[fragment.SentenceID]]
				fragment.Start, fragment.End = boundaries[fragment.Start], boundaries[fragment.End]
			}
		}
	}
	draft.LayoutManifest.ContentHash, draft.LayoutManifest.LayoutValidationHash = "", ""
	// Validate structural coverage before assigning the target identity. The
	// worker must choose the target renderer and compose a fresh proof afterward.
	if _, err = ValidateDocument(draft); err != nil {
		return Document{}, ErrSimplifiedConversion
	}
	draft.ContentLocale = "zh-Hans"
	return draft, nil
}

func (r SimplifiedRunner) convert(parent context.Context, inputs []conversionText) ([]conversionText, error) {
	if !filepath.IsAbs(r.NodePath) || !filepath.IsAbs(r.ScriptPath) {
		return nil, ErrSimplifiedConversion
	}
	raw, err := json.Marshal(inputs)
	if err != nil || len(raw) > MaxDocumentBytes {
		return nil, ErrSimplifiedConversion
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.NodePath, r.ScriptPath)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(raw)
	stdout, stderr := &limitedProcessOutput{limit: MaxDocumentBytes}, &limitedProcessOutput{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil || stdout.overflow || stderr.overflow {
		return nil, errors.Join(ErrSimplifiedConversion, ctx.Err())
	}
	var outputs []conversionText
	if decodeLayoutJSON(stdout.Bytes(), &outputs) != nil || len(outputs) != len(inputs) {
		return nil, ErrSimplifiedConversion
	}
	for i, output := range outputs {
		length := utf8.RuneCountInString(output.Text)
		if !boundedText(output.Text, inputs[i].Text != "") || len(output.Boundaries) != len(inputs[i].Boundaries) || len(output.Boundaries) < 1 || output.Boundaries[0] != 0 || output.Boundaries[len(output.Boundaries)-1] != length {
			return nil, ErrSimplifiedConversion
		}
		for j, n := range output.Boundaries {
			if n < 0 || n > length || (j > 0 && n <= output.Boundaries[j-1]) {
				return nil, ErrSimplifiedConversion
			}
		}
	}
	return outputs, nil
}
