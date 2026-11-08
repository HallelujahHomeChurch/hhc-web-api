package assetclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func illustrationFixture(t *testing.T) ([]byte, Asset) {
	t.Helper()
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body.Bytes())
	return body.Bytes(), Asset{ID: "image", Namespace: "cms.weekly.image", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue", Purpose: "online_bulletin_illustration", Locale: "zh-Hant", Visibility: "private", ChecksumSHA256: hex.EncodeToString(sum[:]), DetectedMIMEType: "image/png", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "not_required"}
}

func TestIllustrationReadUsesOneBoundRequestWithoutGrantWrites(t *testing.T) {
	for _, kind := range []string{"valid", "denied", "hash-mismatch", "mime", "oversized", "invalid-png"} {
		t.Run(kind, func(t *testing.T) {
			data, asset := illustrationFixture(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/priv/assets/image/bulletin-illustration" || r.URL.Query().Get("issueId") != asset.OwnerID || r.URL.Query().Get("checksumSha256") != asset.ChecksumSHA256 || r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
					t.Error("unexpected image request or grant write")
				}
				if kind == "denied" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				switch kind {
				case "hash-mismatch", "invalid-png":
					_, _ = w.Write([]byte("wrong"))
				case "mime":
					w.Header().Set("Content-Type", "application/pdf")
					_, _ = w.Write(data)
				case "oversized":
					_, _ = w.Write(make([]byte, maxBulletinIllustrationBytes+1))
				default:
					_, _ = w.Write(data)
				}
			}))
			defer server.Close()
			client := New(server.URL, "hhc-web-api", "")
			if kind == "invalid-png" {
				sum := sha256.Sum256([]byte("wrong"))
				asset.ChecksumSHA256 = hex.EncodeToString(sum[:])
			}
			for range 2 {
				got, err := client.ReadBulletinIllustration(context.Background(), asset.OwnerID, asset.ID, asset.ChecksumSHA256)
				if kind == "valid" {
					if err != nil || !bytes.Equal(got, data) {
						t.Fatal("owned bytes unavailable", err)
					}
				} else if err == nil || got != nil {
					t.Fatal("unverified image escaped", kind, err)
				}
			}
			if calls != 2 {
				t.Fatal("expected one request per image read", calls)
			}
		})
	}
}

func TestIllustrationRetryUsesOneStableSuccessorForAnExpiredUpload(t *testing.T) {
	data, asset := illustrationFixture(t)
	keys := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var input map[string]any
			if json.NewDecoder(r.Body).Decode(&input) != nil || input["namespace"] != "cms.weekly.image" || input["visibility"] != "private" || input["ownerId"] != asset.OwnerID {
				t.Error("incorrect image ownership/visibility")
			}
			key := r.Header.Get("Idempotency-Key")
			keys[key] = true
			if strings.HasPrefix(key, "online-image-resume:") {
				_ = json.NewEncoder(w).Encode(CreatedUpload{Asset: asset})
			} else {
				_ = json.NewEncoder(w).Encode(CreatedUpload{Asset: Asset{ID: "expired", UploadStatus: "created"}, UploadTarget: UploadTarget{ExpiresAt: time.Now().Add(-time.Hour)}})
			}
			return
		}
		if r.URL.Path != "/priv/assets/"+asset.ID {
			t.Error("completed upload was written again", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(asset)
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "")
	for range 2 {
		got, ready, err := client.EnsureBulletinIllustration(context.Background(), asset.OwnerID, strings.Repeat("a", 64), data)
		if err != nil || !ready || got.ID != asset.ID {
			t.Fatal(got, ready, err)
		}
	}
	if len(keys) != 2 {
		t.Fatal("retry created a new upload instead of resuming", keys)
	}
}
