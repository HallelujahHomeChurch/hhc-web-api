package translation

import "strings"

const gospelDinnerMarker = "綠野仙蹤"

func usesJapaneseNewsTitleRule(request Request) bool {
	return request.Module == "news" && request.SourceLocale == "zh-Hant" && request.TargetLocale == "ja"
}

func applyTitleRule(request Request, source map[string]string, result Result) (Result, error) {
	if !usesJapaneseNewsTitleRule(request) {
		return result, nil
	}
	rule := result.TitleRule
	marked := strings.Contains(source["title"], gospelDinnerMarker)
	if rule == nil {
		if !marked {
			return result, nil
		}
		return Result{}, ErrProvider
	}
	if marked != (rule.Kind == "gospel_dinner") {
		return Result{}, ErrProvider
	}
	if rule.Kind == "none" {
		if rule.Sequence != "" || rule.SourceQualifier != "" || rule.LocalizedQualifier != "" || rule.SourceEventName != "" || rule.LocalizedEventName != "" {
			return Result{}, ErrProvider
		}
		return result, nil
	}
	if !validTitleRuleSource(source["title"], *rule) {
		return Result{}, ErrProvider
	}

	title := "福音食事会"
	if rule.Sequence != "" {
		title = "第" + rule.Sequence + "回" + title
	}
	title += rule.LocalizedQualifier
	if rule.LocalizedEventName != "" {
		title += " - " + rule.LocalizedEventName
	}
	result.Fields["title"] = title
	return result, nil
}

func validTitleRuleSource(source string, rule TitleRuleResult) bool {
	if rule.Kind != "gospel_dinner" || (rule.SourceQualifier == "") != (rule.LocalizedQualifier == "") || (rule.SourceEventName == "") != (rule.LocalizedEventName == "") {
		return false
	}
	if rule.Sequence != "" {
		for _, character := range rule.Sequence {
			if character < '0' || character > '9' {
				return false
			}
		}
		markerIndex := strings.Index(source, gospelDinnerMarker)
		if markerIndex < 0 || !containsDigitRun(source[:markerIndex], rule.Sequence) {
			return false
		}
	}
	if (rule.SourceQualifier != "" && !strings.Contains(source, rule.SourceQualifier)) ||
		(rule.SourceEventName != "" && !strings.Contains(source, rule.SourceEventName)) {
		return false
	}

	remaining := source
	for _, part := range []string{gospelDinnerMarker, "福音餐會", rule.Sequence, rule.SourceQualifier, rule.SourceEventName} {
		if part != "" {
			remaining = strings.ReplaceAll(remaining, part, "")
		}
	}
	return strings.Trim(remaining, " \t\r\n第次回-–—:：·「」『』()（）[]【】") == ""
}

func containsDigitRun(source, sequence string) bool {
	for start := 0; start < len(source); {
		if source[start] < '0' || source[start] > '9' {
			start++
			continue
		}
		end := start + 1
		for end < len(source) && source[end] >= '0' && source[end] <= '9' {
			end++
		}
		if source[start:end] == sequence {
			return true
		}
		start = end
	}
	return false
}
