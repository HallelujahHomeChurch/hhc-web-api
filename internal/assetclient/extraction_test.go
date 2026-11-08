package assetclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExtractionSourceGrantLifecycleAndBoundedPDF(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, kind := range []string{"valid", "consumer-error", "non-pdf", "oversize", "hash-mismatch", "unclean", "incomplete", "processing", "wrong-owner", "forbidden", "unavailable", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			output.Reset()
			pdf := "%PDF-1.7\ncontrolled source"
			sum := sha256.Sum256([]byte(pdf))
			source := Asset{ID: "source/one", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue", Purpose: "weekly_bulletin", Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "not_required", DetectedMIMEType: "application/pdf", Visibility: "private", ChecksumSHA256: hex.EncodeToString(sum[:])}
			var grants, revoked, calls int
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
					t.Error("wrong caller")
				}
				if !strings.Contains(r.URL.EscapedPath(), "source%2Fone") {
					t.Error("unescaped asset ID")
				}
				switch {
				case r.Method == http.MethodDelete:
					revoked++
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost:
					grants++
					var input struct {
						SubjectType, SubjectID, Permission, IdempotencyKey string
						ExpiresAt                                          time.Time
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					if input.SubjectType != "service" || input.SubjectID != "hhc-web-api" || input.Permission != "read" || !strings.HasPrefix(input.IdempotencyKey, fmt.Sprintf("online-extract:job:%d:", grants)) || r.Header.Get("Idempotency-Key") != input.IdempotencyKey || time.Until(input.ExpiresAt) < 59*time.Minute || time.Until(input.ExpiresAt) > time.Hour {
						t.Errorf("grant=%+v", input)
					}
					_ = json.NewEncoder(w).Encode(Grant{ID: "grant", AssetID: source.ID})
				case strings.HasSuffix(r.URL.Path, "/download"):
					if r.Header.Get("X-Asset-Subject-Type") != "service" || r.Header.Get("X-Asset-Subject-Id") != "hhc-web-api" || r.Header.Get("Range") != "" {
						t.Error("wrong download subject")
					}
					w.Header().Set("Content-Type", "application/pdf")
					switch kind {
					case "non-pdf":
						_, _ = w.Write([]byte("not a PDF"))
					case "oversize":
						_, _ = w.Write([]byte("%PDF-" + strings.Repeat("x", 20<<20)))
					case "hash-mismatch":
						_, _ = w.Write([]byte("%PDF-1.7\nchanged"))
					case "forbidden":
						w.WriteHeader(http.StatusForbidden)
					case "unavailable":
						w.WriteHeader(http.StatusServiceUnavailable)
					default:
						_, _ = w.Write([]byte(pdf))
					}
				default:
					actual := source
					if kind == "unclean" {
						actual.ScanStatus = "pending"
					}
					if kind == "incomplete" {
						actual.UploadStatus = "pending"
					}
					if kind == "processing" {
						actual.ProcessingStatus = "pending"
					}
					if kind == "wrong-owner" {
						actual.OwnerID = "other"
					}
					_ = json.NewEncoder(w).Encode(actual)
				}
			}))
			defer server.Close()
			client := New(server.URL, "hhc-web-api", "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := client.WithBulletinSource(ctx, source, "job", 1, func(name string) error {
				calls++
				path = name
				bytes, err := os.ReadFile(name)
				if err != nil || string(bytes) != pdf {
					t.Error("source mismatch")
				}
				if kind == "cancelled" {
					cancel()
					return context.Canceled
				}
				if kind == "consumer-error" {
					return errors.New("private-source-content https://private.invalid?sig=private-secret")
				}
				return nil
			})
			if kind == "valid" {
				if output.Len() != 0 {
					t.Fatal("successful source logged as failure")
				}
			} else {
				stages := map[string]string{"consumer-error": "consume", "cancelled": "consume", "non-pdf": "header", "oversize": "checksum", "hash-mismatch": "checksum", "unclean": "metadata_validation", "incomplete": "metadata_validation", "processing": "metadata_validation", "wrong-owner": "metadata_validation", "forbidden": "download", "unavailable": "download"}
				if !strings.Contains(output.String(), `"stage":"`+stages[kind]+`"`) {
					t.Fatal("source failure stage missing")
				}
				for _, secret := range []string{"private-source-content", "private.invalid", "private-secret", "source/one", "online-extract:", "%PDF-", "hhc-bulletin-extract-"} {
					if strings.Contains(output.String(), secret) {
						t.Fatal("source diagnostic leaked private data")
					}
				}
			}
			if (kind == "valid") != (err == nil) {
				t.Fatalf("err=%v", err)
			}
			if kind == "unclean" || kind == "wrong-owner" || kind == "incomplete" || kind == "processing" {
				if grants != 0 || revoked != 0 || calls != 0 {
					t.Fatal("unsafe asset granted")
				}
			} else if grants != 1 || revoked != 1 {
				t.Fatalf("grants=%d revoked=%d", grants, revoked)
			}
			if path != "" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("temporary PDF retained")
				}
			}
			if kind == "forbidden" && !errors.Is(err, ErrForbidden) {
				t.Fatal(err)
			}
			if kind == "unavailable" && !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
			if kind == "valid" {
				var retryPath string
				if err := client.WithBulletinSource(ctx, source, "job", 2, func(name string) error { retryPath = name; return nil }); err != nil {
					t.Fatal(err)
				}
				if grants != 2 || revoked != 2 {
					t.Fatal("retry did not get and revoke its own grant")
				}
				if _, err := os.Stat(retryPath); !os.IsNotExist(err) {
					t.Fatal("retry retained PDF")
				}
			}
		})
	}
}

func TestExtractionSourceManualRetryDoesNotReplayRevokedGrant(t *testing.T) {
	pdf := "%PDF-1.7\ncontrolled source"
	sum := sha256.Sum256([]byte(pdf))
	source := Asset{ID: "source", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue", Purpose: "weekly_bulletin", Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "not_required", DetectedMIMEType: "application/pdf", Visibility: "private", ChecksumSHA256: hex.EncodeToString(sum[:])}
	keys := map[string]bool{}
	active, revoked := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			key := r.Header.Get("Idempotency-Key")
			if keys[key] {
				// Asset rejects replay when the newly requested expiry differs.
				w.WriteHeader(http.StatusConflict)
				return
			}
			keys[key] = true
			active++
			_ = json.NewEncoder(w).Encode(Grant{ID: fmt.Sprintf("grant-%d", len(keys)), AssetID: source.ID})
		case r.Method == http.MethodDelete:
			active--
			revoked++
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/download"):
			if active != 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte(pdf))
		default:
			_ = json.NewEncoder(w).Encode(source)
		}
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "")
	parserError := errors.New("controlled parser failure")
	for retry := range 2 {
		err := client.WithBulletinSource(context.Background(), source, "same-job", 1, func(path string) error {
			if retry == 0 {
				return parserError
			}
			return nil
		})
		if retry == 0 && !errors.Is(err, parserError) || retry == 1 && err != nil {
			t.Fatalf("manual retry %d: %v", retry, err)
		}
		if active != 0 || revoked != retry+1 {
			t.Fatal("source grant was not revoked")
		}
	}
	if len(keys) != 2 {
		t.Fatal("manual retry reused a source-grant lifecycle")
	}
}
