package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type sourceRepairRepository struct {
	onlineStateRepository
	catalog onlinebulletins.SourceRepairCatalog
	repair  onlinebulletins.PreparedSourceRepair
	writes  int
}

func (repo *sourceRepairRepository) OnlineBulletinSourceParts(context.Context, string) (onlinebulletins.SourceRepairCatalog, error) {
	return repo.catalog, nil
}
func (repo *sourceRepairRepository) SaveOnlineBulletinSourceRepair(_ context.Context, _ string, repair onlinebulletins.PreparedSourceRepair, _ string, _ time.Time) (onlinebulletins.SavedDraft, error) {
	repo.writes++
	repo.repair = repair
	return onlinebulletins.SavedDraft{DocumentID: repo.catalog.DocumentID, Version: 4, Revision: 3, LayoutJobID: "layout"}, nil
}

type sourceRepairAssets struct {
	apiUploads
	asset assetclient.Asset
	reads int
	err   error
}

func (assets *sourceRepairAssets) Get(context.Context, string) (assetclient.Asset, error) {
	return assets.asset, nil
}
func (assets *sourceRepairAssets) ReadBulletinIllustration(context.Context, string, string, string) ([]byte, error) {
	assets.reads++
	return []byte("sanitized-png-fixture"), assets.err
}

func TestSourceRepairRoutesUseSavedSourceAndRejectCallerEvidence(t *testing.T) {
	const issueID = "018f47d2-e5d1-4f3f-8f18-6c8e32621f73"
	for _, scenario := range []struct {
		name   string
		status int
	}{
		{"text", 200}, {"remove", 200}, {"image", 200}, {"scan", 202}, {"owner", 503}, {"read-failure", 503}, {"unprepared", 409}, {"stale", 409}, {"source", 409}, {"revision", 409}, {"part", 400}, {"inject", 400}, {"scope", 403}, {"missing-cas", 428}, {"locale", 404},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			partID, checksum := strings.Repeat("c", 64), strings.Repeat("a", 64)
			repo := &sourceRepairRepository{catalog: onlinebulletins.SourceRepairCatalog{DocumentID: issueID, Version: 3, Revision: 2, CanonicalVersion: 5, SourceChecksum: checksum, Parts: []onlinebulletins.SourcePart{{ID: partID, Kind: "text", Text: "authoritative source", Page: 2, Box: onlinebulletins.Box{X: .1, Y: .2, Width: .5, Height: .02}}}}}
			repo.state = onlinebulletins.AdminState{IssueID: issueID, DocumentID: issueID, Version: 3, CanonicalVersion: 5}
			assets := &sourceRepairAssets{asset: assetclient.Asset{ID: issueID, OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: issueID, Namespace: "cms.weekly.image", Visibility: "private", ChecksumSHA256: checksum, ScanStatus: "clean"}}
			input := map[string]any{"canonicalVersion": 5, "revision": 2, "sourceChecksum": checksum, "partId": partID, "componentId": "body", "alt": "Source image"}
			if scenario.name == "remove" || scenario.name == "image" || scenario.name == "scan" || scenario.name == "owner" || scenario.name == "read-failure" || scenario.name == "unprepared" {
				repo.catalog.Parts[0].Kind = "image"
				repo.catalog.Images = map[string]onlinebulletins.SourceImage{partID: {AssetID: issueID, SHA256: checksum, SizeBytes: len("sanitized-png-fixture")}}
			}
			switch scenario.name {
			case "remove":
				input["remove"] = true
				repo.catalog.Images = nil
				assets.err = errors.New("unavailable")
			case "scan":
				assets.asset.ScanStatus = "pending"
			case "owner":
				assets.asset.OwnerID = "different-issue"
			case "read-failure":
				assets.err = errors.New("unavailable")
			case "unprepared":
				repo.catalog.Images = nil
			case "stale":
				repo.catalog.Version = 4
			case "revision":
				input["revision"] = 1
			case "source":
				input["sourceChecksum"] = strings.Repeat("b", 64)
			case "part":
				input["partId"] = strings.Repeat("d", 64)
			case "inject":
				input["box"] = map[string]float64{"x": .1}
			}
			body, _ := json.Marshal(input)
			locale := "zh-Hant"
			if scenario.name == "locale" {
				locale = "zh-Hans"
			}
			request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/"+issueID+"/online/general/"+locale+"/source-repairs", strings.NewReader(string(body)))
			trustedBulletinAudit(request, "cms:bulletins:write")
			request.Header.Set("If-Match", `"3"`)
			if scenario.name == "scope" {
				trustedBulletinAudit(request, "cms:bulletins:read")
			}
			if scenario.name == "missing-cas" {
				request.Header.Del("If-Match")
			}
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, assets, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("cacheable source repair")
			}
			if scenario.name == "remove" && (assets.reads != 0 || repo.repair.Illustration != nil) {
				t.Fatal("removal depended on unavailable asset")
			}
			if scenario.status == 200 {
				if repo.writes != 1 || repo.repair.Input.OnlineVersion != 3 || repo.repair.Part.ID != partID || repo.repair.RendererSHA256 != onlinebulletins.RendererV11SHA256 {
					t.Fatal("lost source/version binding")
				}
			} else if repo.writes != 0 {
				t.Fatal("failed/pending repair changed draft")
			}
		})
	}
}

func TestIllustrationHydrationFailsBeforeReturningUnverifiedBytes(t *testing.T) {
	assets := &sourceRepairAssets{err: errors.New("scan unavailable")}
	h := &Handler{uploads: assets}
	components := []onlinebulletins.Component{{Illustrations: []onlinebulletins.Illustration{{ID: "source", SHA256: strings.Repeat("a", 64), SizeBytes: 20}}}}
	if err := h.hydrateBulletinIllustrations(context.Background(), "issue", components, map[string]string{"source": "owned-asset"}); err == nil || components[0].Illustrations[0].PNG != "" {
		t.Fatal("unverified bytes entered response")
	}
}
