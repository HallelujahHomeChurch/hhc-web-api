package onlinebulletins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Continuation is derived only from the preceding verified page, never a provider claim.
type RecognitionContinuation struct {
	Group          string   `json:"group"`
	Anchor         string   `json:"anchor"`
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	Speakers       []string `json:"speakers"`
	PreviousSHA256 string   `json:"previousSha256"`
}

func recognitionSemanticText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

// Segmentation and provider group spelling may vary; source scalar ownership may not.
func recognitionSemanticSHA256(input RecognitionPage, result RecognitionResult) (string, bool) {
	type article struct {
		anchor, kind, title string
		speakers            []string
		prose, separator    bool
	}
	groups := map[string]*article{}
	if c := input.Continuation; c != nil && c.Group != "" {
		groups[c.Group] = &article{anchor: c.Anchor, kind: c.Kind, title: c.Title, speakers: c.Speakers, prose: true}
	}
	var articles []*article
	assignments := map[string]string{}
	var order []string
	otherGroups := map[string]string{}
	for _, block := range result.Blocks {
		role := block.Role
		omitted := role == "fixed_label" || role == "body_date" || role == "body_transcriber" || role == "body_editor"
		value := block.Text
		if block.Mode == "native" && len(block.SourceIDs)+len(block.SourceRanges) > 0 {
			var ok bool
			value, _, ok = recognitionNativeValue(input, block)
			if !ok {
				return "", false
			}
		}
		value = recognitionSemanticText(value)
		if strings.HasPrefix(role, "body_") && !omitted && value == "" {
			return "", false
		}
		if (role == "body_title" || role == "body_speaker") && strings.ContainsAny(value, "~～〜") {
			return "", false
		}
		refs, ok := recognitionReferences(input, block)
		if !ok {
			return "", false
		}
		var scalars []string
		for _, ref := range refs {
			var text []rune
			for _, native := range input.Native {
				if native.ID == ref.ID {
					text = []rune(native.Text)
					break
				}
			}
			for i := ref.Start; i < ref.End; i++ {
				if !unicode.IsSpace(text[i]) {
					scalars = append(scalars, fmt.Sprintf("%s:%d", ref.ID, i))
				}
			}
		}
		ownerAnchor := ""
		if strings.HasPrefix(role, "body_") && !omitted {
			if role == "body_title" {
				if groups[block.Group] != nil {
					return "", false
				}
				anchor := strings.Join(scalars, ",")
				if anchor == "" {
					anchor = fmt.Sprintf("p%d-visual-%d:%s", input.Page, len(articles), value)
				}
				group := &article{anchor: anchor, kind: block.SectionKind, title: value}
				groups[block.Group] = group
				articles = append(articles, group)
			}
			owner := groups[block.Group]
			if owner == nil {
				return "", false
			}
			switch role {
			case "body_speaker":
				if owner.prose || slices.Contains(owner.speakers, value) {
					return "", false
				}
				owner.speakers = append(owner.speakers, value)
			case "body_separator":
				if owner.prose || strings.Trim(value, "~～〜") != "" || owner.separator {
					return "", false
				}
				owner.separator = true
			case "body_paragraph":
				owner.prose = true
			}
			ownerAnchor = owner.anchor
		} else if !omitted && (strings.HasPrefix(role, "hymn_") || role == "cover_work" || role == "cover_word" || role == "cover_worship" || role == "back_summary" || role == "announcement" || role == "prayer") {
			family := role
			if strings.HasPrefix(role, "hymn_") {
				family = "hymn"
			}
			key := family + ":" + block.Group
			if otherGroups[key] == "" {
				anchor := fmt.Sprintf("p%d-visual:%s", input.Page, value)
				if len(scalars) > 0 {
					anchor = scalars[0]
				}
				otherGroups[key] = anchor
			}
			ownerAnchor = otherGroups[key]
		}
		classification := role + ":" + ownerAnchor
		if omitted {
			classification = "omitted"
		}
		for _, scalar := range scalars {
			assignments[scalar] = classification
			if !omitted {
				order = append(order, scalar+":"+classification)
			}
		}
		for _, imageID := range block.ImageIDs {
			key := "image:" + imageID
			assignments[key] = classification
			if !omitted {
				order = append(order, key+":"+classification)
			}
		}
		if block.Mode == "visual" && !omitted && role != "illustration" {
			order = append(order, "visual:"+classification+":"+value)
		}

	}
	var inventory []any
	for _, a := range articles {
		if a.separator && len(a.speakers) == 0 {
			return "", false
		}
		inventory = append(inventory, []any{a.anchor, a.kind, a.title, a.speakers})
	}
	raw, _ := json.Marshal([]any{input.SourceSHA256, input.Page, input.Continuation, inventory, assignments, order})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), true
}

func NextRecognitionContinuation(input RecognitionPage, result RecognitionResult) (*RecognitionContinuation, error) {
	signature, ok := recognitionSemanticSHA256(input, result)
	if !ok || result.SemanticSHA256 != signature {
		return nil, ErrRecognition
	}
	next := RecognitionContinuation{Speakers: []string{}}
	if input.Continuation != nil {
		next = *input.Continuation
		next.Speakers = slices.Clone(next.Speakers)
	}
	for _, block := range result.Blocks {
		switch block.Role {
		case "body_title":
			refs, _ := recognitionReferences(input, block)
			anchor := fmt.Sprintf("p%d:%s", input.Page, block.Group)
			if len(refs) > 0 {
				anchor = fmt.Sprintf("%s:%d", refs[0].ID, refs[0].Start)
			}
			next.Group, next.Anchor, next.Kind, next.Title, next.Speakers = block.Group, anchor, block.SectionKind, recognitionSemanticText(block.Text), []string{}
		case "body_speaker":
			if block.Group == next.Group {
				next.Speakers = append(next.Speakers, recognitionSemanticText(block.Text))
			}
		}
	}
	raw, _ := json.Marshal([]any{input, result})
	digest := sha256.Sum256(raw)
	next.PreviousSHA256 = hex.EncodeToString(digest[:])
	return &next, nil
}
