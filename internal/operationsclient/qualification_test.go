package operationsclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQualificationValidatesExactSubjectAndVersion(t *testing.T) {
	subject := "01995fd2-6f6c-7b40-8707-6503edc362c8"
	for _, body := range []string{`{"subjectId":"` + subject + `","qualified":true,"version":"` + strings.Repeat("a", 64) + `"}`, `{"subjectId":"other","qualified":true,"version":"` + strings.Repeat("a", 64) + `"}`, `{"subjectId":"` + subject + `","version":"` + strings.Repeat("a", 64) + `"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		got, err := New(server.URL).CheckMemberQualification(t.Context(), subject)
		server.Close()
		if strings.Contains(body, `"qualified":true`) && !strings.Contains(body, "other") {
			if err != nil || !got.Qualified {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		} else if err == nil {
			t.Fatalf("invalid response accepted %s", body)
		}
	}
}
