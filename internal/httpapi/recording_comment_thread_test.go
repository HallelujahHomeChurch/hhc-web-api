package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestCommentQueryThreadedViewIsExplicitAndStrict(t *testing.T) {
	for _, tc := range []struct {
		query string
		valid bool
	}{
		{"", true},
		{"view=threaded&order=top", true},
		{"view=threaded&parentId=018f0000-0000-4000-8000-000000000001&order=oldest&limit=20", true},
		{"view=", false},
		{"view=flat", false},
		{"view=threaded&view=threaded", false},
		{"view=threaded&unknown=1", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			_, _, err := commentQuery(httptest.NewRequest("GET", "/comments?"+tc.query, nil))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
