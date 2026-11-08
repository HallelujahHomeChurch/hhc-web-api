package onlinebulletins

import "unicode"

// PDF CJK spacing is layout, but a Latin word separator remains meaningful.
func layoutText(text string) ([]rune, []int) {
	runes := []rune(text)
	normalized, offsets := []rune{}, []int{}
	for i := 0; i < len(runes); i++ {
		if unicode.IsSpace(runes[i]) {
			start := i
			for i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
				i++
			}
			if start > 0 && i+1 < len(runes) && !unicode.Is(unicode.Han, runes[start-1]) && !unicode.Is(unicode.Han, runes[i+1]) {
				for j := start; j <= i; j++ {
					normalized, offsets = append(normalized, runes[j]), append(offsets, j)
				}
			}
			continue
		}
		normalized, offsets = append(normalized, runes[i]), append(offsets, i)
	}
	return normalized, offsets
}

func layoutIdentityText(text string) string {
	runes, _ := layoutText(text)
	return string(runes)
}

// ReanchorWhitespaceRange maps exact code points only on the same sentence ID.
// Text edits and whitespace-only selections fail closed instead of widening.
func ReanchorWhitespaceRange(before, after string, start, end int) (int, int, bool) {
	if start < 0 || end <= start || end > len([]rune(before)) {
		return 0, 0, false
	}
	if before == after {
		return start, end, true
	}
	old, oldOffsets := layoutText(before)
	next, newOffsets := layoutText(after)
	if string(old) != string(next) {
		return 0, 0, false
	}
	first, last := -1, -1
	for i, offset := range oldOffsets {
		if offset >= start && offset < end {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0, 0, false
	}
	return newOffsets[first], newOffsets[last] + 1, true
}
