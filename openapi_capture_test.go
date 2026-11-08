package hhcwebapi

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCaptureContractAndFixtures(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	base := "/admin/recordings/{id}/captures"
	for _, route := range []struct{ path, method string }{
		{base, "POST"}, {base + "/{captureId}", "GET"},
		{base + "/{captureId}/objects", "POST"}, {base + "/{captureId}/sign", "POST"},
		{base + "/{captureId}/confirm", "POST"}, {base + "/{captureId}/stop", "POST"},
		{base + "/{captureId}/seal", "POST"}, {base + "/{captureId}/abort", "POST"},
		{base + "/{captureId}/auto-publish", "DELETE"}, {base + "/{captureId}/live", "DELETE"},
		{"/member/recordings/live", "GET"}, {"/member/recordings/{id}/live/playback", "POST"},
	} {
		path := doc.Paths.Find(route.path)
		if path == nil || path.GetOperation(route.method) == nil {
			t.Fatalf("missing %s %s", route.method, route.path)
		}
		op := path.GetOperation(route.method)
		for _, code := range []string{"400", "401", "403", "404", "409", "410", "429", "503"} {
			if op.Responses.Value(code) == nil {
				t.Errorf("%s missing %s", route.path, code)
			}
		}
	}
	raw, err := os.ReadFile("docs/obs-capture/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
		Valid  bool   `json:"valid"`
		Value  any    `json:"value"`
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 12 {
		t.Fatal("missing lifecycle and rejection examples")
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			err := compileOpenAPISchema(t, f.Schema).Validate(f.Value)
			if (err == nil) != f.Valid {
				t.Fatalf("valid=%v: %v", f.Valid, err)
			}
		})
	}
}
