package hhcwebapi

import (
	"github.com/getkin/kin-openapi/openapi3"
	"testing"
)

// Remove only the explicitly checked additive D2 layer before comparing frozen
// D1/K4/S10 artifacts. The original artifact files and fields stay immutable.
func frozenD1ControlOperation(t *testing.T, op *openapi3.Operation) {
	t.Helper()
	found := false
	for _, p := range op.Parameters {
		if p.Value.Name == "controlVersion" {
			found = true
			v := p.Value
			if v.In != "query" || v.Required || len(v.Schema.Value.Enum) != 2 || v.Schema.Value.Enum[0] != "d1-2026-10-17.rc2" || v.Schema.Value.Enum[1] != "d2-2026-10-23.rc1" {
				t.Fatal("invalid additive D2 negotiation")
			}
			v.Schema.Value.Enum = v.Schema.Value.Enum[:1]
		}
	}
	if !found {
		t.Fatal("missing control negotiation")
	}
	media := op.Responses.Value("200").Value.Content["application/json"]
	variants := media.Schema.Value.OneOf
	if len(variants) != 3 || variants[0].Ref != "#/components/schemas/BroadcastControlEnvelope" || variants[1].Ref != "#/components/schemas/BroadcastControlD1Envelope" || variants[2].Ref != "#/components/schemas/BroadcastControlD2Envelope" {
		t.Fatal("invalid additive D2 variants")
	}
	media.Schema.Value.OneOf = variants[:2]
}
func frozenS10OutputInput(t *testing.T, doc *openapi3.T) {
	t.Helper()
	schema := doc.Components.Schemas["BroadcastOutputOpenInput"].Value
	p := schema.Properties["recoveryVersion"]
	if p == nil || p.Value == nil || !p.Value.Type.Is("string") || len(p.Value.Enum) != 2 || p.Value.Enum[0] != "" || p.Value.Enum[1] != "common-watermark-v1" {
		t.Fatal("invalid recovery preference contract")
	}
	for _, key := range schema.Required {
		if key == "recoveryVersion" {
			t.Fatal("recovery preference became required")
		}
	}
	delete(schema.Properties, "recoveryVersion")
}
func TestD2ControlSchemaAcceptsLegacyAndRecoverySnapshots(t *testing.T) {
	schema := compileCandidateSchema(t, "openapi.yaml", "BroadcastControlD2")
	base := map[string]any{"revision": float64(1), "phase": "preview", "pendingCommand": nil, "controlVersion": "d2-2026-10-23.rc1", "recordingId": "018f0000-0000-4000-8000-000000000001", "captureId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "epoch": float64(1), "boundaries": map[string]any{"revision": float64(1), "startSequence": nil, "endSequenceExclusive": nil}, "recovery": map[string]any{"version": nil, "state": "unsupported", "deadline": nil, "lastCommonConfirmedAt": nil, "commonConfirmedEndSequenceExclusive": nil}, "publicEnd": nil}
	if e := schema.Validate(base); e != nil {
		t.Fatal(e)
	}
	base["recovery"] = map[string]any{"version": "common-watermark-v1", "state": "ended", "deadline": "2026-10-23T13:05:00Z", "lastCommonConfirmedAt": "2026-10-23T13:00:00Z", "commonConfirmedEndSequenceExclusive": float64(10)}
	base["publicEnd"] = map[string]any{"reason": "idle_timeout", "endSequenceExclusive": float64(10)}
	if e := schema.Validate(base); e != nil {
		t.Fatal(e)
	}
	base["publicEnd"] = map[string]any{"reason": "invented", "endSequenceExclusive": nil}
	if e := schema.Validate(base); e == nil {
		t.Fatal("unknown public end accepted")
	}
}
