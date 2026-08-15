package translation

import (
	"errors"
	"reflect"
	"testing"
)

func TestApplyTitleRuleRendersJapaneseGospelDinnerTitles(t *testing.T) {
	tests := []struct {
		name   string
		source string
		rule   TitleRuleResult
		want   string
	}{
		{
			name:   "standard",
			source: "432次綠野仙蹤福音餐會 - 璨恩的尋根",
			rule:   TitleRuleResult{Kind: "gospel_dinner", Sequence: "432", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"},
			want:   "第432回福音食事会 - 璨恩のルーツ探し",
		},
		{
			name:   "prefixed occurrence",
			source: "第432次綠野仙蹤福音餐會 - 璨恩的尋根",
			rule:   TitleRuleResult{Kind: "gospel_dinner", Sequence: "432", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"},
			want:   "第432回福音食事会 - 璨恩のルーツ探し",
		},
		{
			name:   "anniversary without occurrence",
			source: "綠野仙蹤福音餐會十週年 - 璨恩的尋根",
			rule:   TitleRuleResult{Kind: "gospel_dinner", SourceQualifier: "十週年", LocalizedQualifier: "10周年", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"},
			want:   "福音食事会10周年 - 璨恩のルーツ探し",
		},
		{
			name:   "quoted event name",
			source: "432次綠野仙蹤福音餐會「璨恩的尋根」",
			rule:   TitleRuleResult{Kind: "gospel_dinner", Sequence: "432", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"},
			want:   "第432回福音食事会 - 璨恩のルーツ探し",
		},
		{
			name:   "no event name",
			source: "第432次綠野仙蹤福音餐會",
			rule:   TitleRuleResult{Kind: "gospel_dinner", Sequence: "432"},
			want:   "第432回福音食事会",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := Request{Module: "news", SourceLocale: "zh-Hant", TargetLocale: "ja", Fields: map[string]string{"title": test.source}}
			result, err := applyTitleRule(request, request.Fields, Result{Fields: map[string]string{"title": "model full title"}, TitleRule: &test.rule})
			if err != nil {
				t.Fatal(err)
			}
			if result.Fields["title"] != test.want {
				t.Fatalf("title = %q, want %q", result.Fields["title"], test.want)
			}
		})
	}
}

func TestApplyTitleRuleRejectsInconsistentModelMetadata(t *testing.T) {
	tests := []struct {
		name   string
		source string
		rule   *TitleRuleResult
	}{
		{name: "missing metadata", source: "432次綠野仙蹤福音餐會 - 璨恩的尋根"},
		{name: "missed marker", source: "432次綠野仙蹤福音餐會 - 璨恩的尋根", rule: &TitleRuleResult{Kind: "none"}},
		{name: "false marker", source: "一般消息 - 璨恩的尋根", rule: &TitleRuleResult{Kind: "gospel_dinner", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"}},
		{name: "invented occurrence", source: "432次綠野仙蹤福音餐會 - 璨恩的尋根", rule: &TitleRuleResult{Kind: "gospel_dinner", Sequence: "442", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"}},
		{name: "non digit occurrence", source: "第四百三十二次綠野仙蹤福音餐會", rule: &TitleRuleResult{Kind: "gospel_dinner", Sequence: "四百三十二"}},
		{name: "invented qualifier", source: "綠野仙蹤福音餐會 - 璨恩的尋根", rule: &TitleRuleResult{Kind: "gospel_dinner", SourceQualifier: "十週年", LocalizedQualifier: "10周年", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"}},
		{name: "unpaired qualifier", source: "綠野仙蹤福音餐會十週年", rule: &TitleRuleResult{Kind: "gospel_dinner", SourceQualifier: "十週年"}},
		{name: "invented event name", source: "綠野仙蹤福音餐會 - 璨恩的尋根", rule: &TitleRuleResult{Kind: "gospel_dinner", SourceEventName: "另一個名稱", LocalizedEventName: "別の名前"}},
		{name: "unpaired event name", source: "綠野仙蹤福音餐會 - 璨恩的尋根", rule: &TitleRuleResult{Kind: "gospel_dinner", SourceEventName: "璨恩的尋根"}},
		{name: "unknown kind", source: "綠野仙蹤福音餐會", rule: &TitleRuleResult{Kind: "other"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := Request{Module: "news", SourceLocale: "zh-Hant", TargetLocale: "ja", Fields: map[string]string{"title": test.source}}
			_, err := applyTitleRule(request, request.Fields, Result{Fields: map[string]string{"title": "model full title"}, TitleRule: test.rule})
			if !errors.Is(err, ErrProvider) {
				t.Fatalf("error = %v, want ErrProvider", err)
			}
		})
	}
}

func TestApplyTitleRuleLeavesUnrelatedTranslationsUnchanged(t *testing.T) {
	want := Result{Fields: map[string]string{"title": "Translated title"}}
	requests := []Request{
		{Module: "news", SourceLocale: "zh-Hant", TargetLocale: "en"},
		{Module: "history", SourceLocale: "zh-Hant", TargetLocale: "ja"},
	}
	for _, request := range requests {
		got, err := applyTitleRule(request, map[string]string{"title": "綠野仙蹤"}, want)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("request=%#v result=%#v error=%v", request, got, err)
		}
	}

	request := Request{Module: "news", SourceLocale: "zh-Hant", TargetLocale: "ja"}
	withoutMetadata, err := applyTitleRule(request, map[string]string{"title": "一般消息"}, Result{Fields: map[string]string{"title": "一般のお知らせ"}})
	if err != nil || withoutMetadata.Fields["title"] != "一般のお知らせ" {
		t.Fatalf("result=%#v error=%v", withoutMetadata, err)
	}

	got, err := applyTitleRule(request, map[string]string{"title": "一般消息"}, Result{Fields: map[string]string{"title": "一般のお知らせ"}, TitleRule: &TitleRuleResult{Kind: "none"}})
	if err != nil || got.Fields["title"] != "一般のお知らせ" {
		t.Fatalf("result=%#v error=%v", got, err)
	}
}
