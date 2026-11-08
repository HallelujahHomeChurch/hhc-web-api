package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeRequiresOneBoundedUTF8JSONObject(t *testing.T) {
	for _, body := range []string{"null", "[]", `{} {}`, "{} trailing", "{\"title\":\"\xff\"}", strings.Repeat(" ", 1<<20) + "{}"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var input struct {
			Title string `json:"title"`
		}
		if decode(w, r, &input) || w.Code != http.StatusBadRequest {
			t.Fatalf("invalid object accepted (bytes=%d, status=%d)", len(body), w.Code)
		}
	}
	for _, body := range []string{`{}`, " \n{\"title\":\"聚會\"}\t"} {
		var input struct {
			Title string `json:"title"`
		}
		if !decode(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), &input) {
			t.Fatal("valid object rejected")
		}
	}
}
