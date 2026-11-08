package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type bulletinIllustrationAssets interface {
	ReadBulletinIllustration(context.Context, string, string, string) ([]byte, error)
}

func (h *Handler) adminOnlineSourceParts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	catalog, err := h.service.OnlineSourceParts(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"))
	if err != nil {
		handleError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.online.read", catalog.DocumentID, auditclient.Metadata{}) {
		return
	}
	for i := range catalog.Parts {
		part := &catalog.Parts[i]
		image := catalog.Images[part.ID]
		if part.Kind != "image" || image.AssetID == "" {
			continue
		}
		assets, ok := h.uploads.(bulletinIllustrationAssets)
		if !ok {
			continue
		}
		if data, err := assets.ReadBulletinIllustration(r.Context(), r.PathValue("issueID"), image.AssetID, image.SHA256); err == nil && len(data) == image.SizeBytes {
			part.PNG = base64.StdEncoding.EncodeToString(data)
		}
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, catalog.Version))
	writeData(w, http.StatusOK, catalog, nil)
}

func (h *Handler) adminOnlineSourceRepair(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	var input onlinebulletins.SourceRepairInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	input.OnlineVersion = expected
	issueID := r.PathValue("issueID")
	catalog, err := h.service.OnlineSourceParts(r.Context(), issueID, r.PathValue("series"), r.PathValue("contentLocale"))
	if err != nil {
		handleError(w, err)
		return
	}
	if catalog.Version != expected || catalog.CanonicalVersion != input.CanonicalVersion || catalog.Revision != input.Revision || catalog.SourceChecksum != input.SourceChecksum {
		h.onlineWriteError(w, r, bulletins.ErrPrecondition)
		return
	}
	repair := onlinebulletins.PreparedSourceRepair{Input: input, RendererSHA256: onlinebulletins.RendererV11SHA256}
	for _, part := range catalog.Parts {
		if part.ID == input.PartID {
			repair.Part = part
			break
		}
	}
	if repair.Part.ID == "" {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	if repair.Part.Kind == "image" && !input.Remove {
		assets, ok := h.uploads.(bulletinIllustrationAssets)
		if !ok {
			handleError(w, assetclient.ErrUnavailable)
			return
		}
		image := catalog.Images[input.PartID]
		if image.AssetID == "" {
			writeError(w, http.StatusConflict, "source_reconversion_required", "Reconvert this source before restoring its images.")
			return
		}
		asset, err := h.uploads.Get(r.Context(), image.AssetID)
		if err != nil {
			handleError(w, err)
			return
		}
		if asset.OwnerService != "hhc-web-api" || asset.OwnerType != "bulletin_issue" || asset.OwnerID != issueID || asset.Namespace != "cms.weekly.image" || asset.Visibility != "private" || asset.ChecksumSHA256 != image.SHA256 {
			handleError(w, assetclient.ErrUnavailable)
			return
		}
		if asset.ScanStatus == "pending" {
			w.Header().Set("Retry-After", "5")
			writeData(w, http.StatusAccepted, onlinebulletins.SourceRepairResult{Status: "waitingForScan"}, nil)
			return
		}
		png, err := assets.ReadBulletinIllustration(r.Context(), issueID, image.AssetID, image.SHA256)
		if err != nil || len(png) != image.SizeBytes {
			handleError(w, assetclient.ErrUnavailable)
			return
		}
		repair.Illustration = &onlinebulletins.Illustration{AssetID: image.AssetID, SHA256: image.SHA256, SizeBytes: image.SizeBytes}
	}
	saved, err := h.service.RepairOnlineSource(r.Context(), issueID, repair, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, saved.Version))
	writeData(w, http.StatusOK, onlinebulletins.SourceRepairResult{Status: "saved", Draft: &saved}, nil)
}

func (h *Handler) hydrateBulletinIllustrations(ctx context.Context, issueID string, components []onlinebulletins.Component, memberAssets map[string]string) error {
	cache := map[string]string{}
	var failure error
	for i := range components {
		for j := range components[i].Illustrations {
			picture := &components[i].Illustrations[j]
			assetID := picture.AssetID
			if memberAssets != nil {
				assetID = memberAssets[picture.ID]
			}
			picture.AssetID = ""
			key := assetID + ":" + picture.SHA256
			if cached, ok := cache[key]; ok {
				picture.PNG = cached
				continue
			}
			assets, ok := h.uploads.(interface {
				ReadBulletinIllustration(context.Context, string, string, string) ([]byte, error)
			})
			if !ok {
				failure = assetclient.ErrUnavailable
				continue
			}
			data, err := assets.ReadBulletinIllustration(ctx, issueID, assetID, picture.SHA256)
			if err != nil || len(data) != picture.SizeBytes {
				failure = assetclient.ErrUnavailable
				continue
			}
			picture.PNG = base64.StdEncoding.EncodeToString(data)
			cache[key] = picture.PNG
		}
	}
	return failure
}
