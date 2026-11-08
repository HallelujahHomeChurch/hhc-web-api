package httpapi

import (
	"github.com/DATA-DOG/go-sqlmock"
	"net/http/httptest"
	"testing"
)

func TestReadinessDoesNotCallUnavailableDatabase(t *testing.T) {
	db, _, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	h := New(nil, db, nil).Routes()
	for _, path := range []string{"/ready", "/health/ready"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("dependency outage evicted AP: %d", w.Code)
		}
	}
}
