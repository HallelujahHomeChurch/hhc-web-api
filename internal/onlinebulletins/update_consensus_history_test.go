package onlinebulletins

import "testing"

func TestUpdateConsensusHistoricalDisagreementWithRetainedSameID(t *testing.T) {
	input := updateFixture(t)
	previous := validDocument(4)
	input.Published = &previous
	semanticMappingRename(&input.Incoming, "sentence-body-title", "sentence-body-title", "Different article, same named speaker")
	first, second := readyUpdatePlan(), readyUpdatePlan()
	first.Mappings = []SemanticMappingDecision{{FromSentenceID: "sentence-speaker", Kind: "historical", ToSentenceIDs: []string{}}}
	for _, kind := range []string{"equivalent", "omitted", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			second.Mappings = []SemanticMappingDecision{}
			if kind == "equivalent" {
				second.Mappings = []SemanticMappingDecision{{FromSentenceID: "sentence-speaker", Kind: "equivalent", ToSentenceIDs: []string{"sentence-speaker"}}}
			}
			if kind == "invalid" {
				first.Mappings = []SemanticMappingDecision{{FromSentenceID: "sentence-speaker", Kind: "equivalent", ToSentenceIDs: []string{"invented-target"}}}
				second.Mappings = first.Mappings
			}
			plan, err := updateConsensus(input, first, second)
			if err != nil {
				t.Fatal(err)
			}
			_, mappings, err := ApplyUpdatePlan(input, plan)
			if err != nil {
				t.Fatal(err)
			}
			got := semanticMappingFor(t, mappings, "sentence-speaker")

			if got.Status != "removed" {
				t.Fatalf("mapping disagreement retained old notes on changed ownership: %+v", got)
			}
		})
	}
}
