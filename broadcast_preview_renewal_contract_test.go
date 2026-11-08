package hhcwebapi

import (
	"github.com/getkin/kin-openapi/openapi3"
	"testing"
)

// The optional renewal input extends canonical requests only. Preserve frozen
// B1 artifacts and verify every pre-existing property unchanged afterward.
func frozenPreviewInput(t *testing.T, doc *openapi3.T) {
	t.Helper()
	s := doc.Components.Schemas["BroadcastPreviewInput"].Value
	p := s.Properties["playbackScopeId"]
	if p == nil || p.Value == nil || !p.Value.Type.Is("string") || p.Value.Format != "uuid" {
		t.Fatal("missing typed preview renewal scope")
	}
	for _, key := range s.Required {
		if key == "playbackScopeId" {
			t.Fatal("renewal scope became mandatory")
		}
	}
	delete(s.Properties, "playbackScopeId")
}
func TestPreviewRenewalInputSchema(t *testing.T) {
	schema := compileCandidateSchema(t, "openapi.yaml", "BroadcastPreviewInput")
	body := map[string]any{"captureId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "epoch": float64(1)}
	if e := schema.Validate(body); e != nil {
		t.Fatal(e)
	}
	body["playbackScopeId"] = "018f0000-0000-4000-8000-000000000001"
	if e := schema.Validate(body); e != nil {
		t.Fatal(e)
	}
	body["playbackScopeId"] = float64(1)
	if e := schema.Validate(body); e == nil {
		t.Fatal("numeric scope accepted")
	}
}
