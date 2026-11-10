package assetclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestValidateHomeBanner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		width   int
		status  int
		corrupt bool
		want    error
	}{
		{"exact", 1900, 200, false, nil}, {"wrong dimensions", 1899, 200, false, ErrInvalid},
		{"changed bytes", 1900, 200, true, ErrInvalid}, {"dependency", 1900, 503, false, ErrUnavailable},
		{"authentication", 1900, 401, false, ErrForbidden}, {"missing", 1900, 404, false, ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer
			if err := jpeg.Encode(&data, image.NewGray(image.Rect(0, 0, tc.width, 700)), nil); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data.Bytes())
			checksum := hex.EncodeToString(digest[:])
			if tc.corrupt {
				checksum = string(bytes.Repeat([]byte("0"), 64))
			}
			revoked := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost:
					var body struct {
						SubjectType string    `json:"subjectType"`
						SubjectID   string    `json:"subjectId"`
						ExpiresAt   time.Time `json:"expiresAt"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.SubjectType != "service" || body.SubjectID != "hhc-web-api" || time.Until(body.ExpiresAt) <= 0 || time.Until(body.ExpiresAt) > time.Minute {
						t.Errorf("unbounded grant: %#v", body)
					}
					_ = json.NewEncoder(w).Encode(Grant{ID: "grant", AssetID: "banner"})
				case r.Method == http.MethodDelete:
					revoked = true
					w.WriteHeader(204)
				case r.URL.Path == "/priv/assets/banner/download":
					if r.Header.Get("Range") != "" {
						t.Error("partial validation")
					}
					w.Header().Set("Content-Type", "image/jpeg")
					w.WriteHeader(tc.status)
					_, _ = w.Write(data.Bytes())
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			err := New(srv.URL, "hhc-web-api", "").ValidateHomeBanner(context.Background(), "banner", checksum)
			if !errors.Is(err, tc.want) || !revoked {
				t.Fatalf("err=%v want=%v revoked=%v", err, tc.want, revoked)
			}
		})
	}
}
