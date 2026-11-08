package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type onlineExtractionSnapshot struct {
	Candidate onlinebulletins.DocumentCandidate `json:"candidate"`
	Canonical onlinebulletins.CanonicalMetadata `json:"canonicalMetadata"`
	Issues    []onlinebulletins.ReviewIssue     `json:"issues"`
}

func (r *Repository) OnlineBulletinSourceParts(ctx context.Context, issueID string) (onlinebulletins.SourceRepairCatalog, error) {
	var catalog onlinebulletins.SourceRepairCatalog
	var revision sql.NullInt64
	var metadata onlinebulletins.CanonicalMetadata
	var raw []byte
	var checksum string
	// Read only repair authority and its immutable evidence in one MVCC snapshot.
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(d.id::text,''),COALESCE(d.version,0),rev.revision,i.version,
	 COALESCE(rev.source_asset_checksum,''),v.title,v.subtitle,i.issue_number,i.issue_date::text,
	 evidence.snapshot_json,COALESCE(evidence.snapshot_sha256,'')
	 FROM hhc_web.bulletin_issue i
	 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant'
	 LEFT JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series=v.series AND d.content_locale=v.locale
	 LEFT JOIN hhc_web.online_bulletin_revision rev ON rev.document_id=d.id AND rev.revision=d.local_revision
	 LEFT JOIN LATERAL (
	  SELECT j.snapshot_json,j.snapshot_sha256 FROM hhc_web.online_bulletin_extraction_job j
	  WHERE j.document_id=d.id AND j.snapshot_json IS NOT NULL
	  AND j.source_asset_checksum=rev.source_asset_checksum
	  AND j.source_asset_checksum=v.pdf_sha256 AND j.source_asset_id=v.pdf_asset_id
	  AND j.snapshot_json->'canonicalMetadata'=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)
	  ORDER BY j.created_at DESC,j.id DESC LIMIT 1
	 ) evidence ON true WHERE i.id=$1`, issueID).Scan(&catalog.DocumentID, &catalog.Version, &revision, &catalog.CanonicalVersion, &catalog.SourceChecksum, &metadata.Title, &metadata.Subtitle, &metadata.IssueNumber, &metadata.Date, &raw, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return onlinebulletins.SourceRepairCatalog{}, bulletins.ErrNotFound
	}
	if err != nil {
		return onlinebulletins.SourceRepairCatalog{}, err
	}
	if !revision.Valid || len(raw) == 0 {
		return onlinebulletins.SourceRepairCatalog{}, bulletins.ErrConflict
	}
	catalog.Revision = revision.Int64
	var snapshot onlineExtractionSnapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return onlinebulletins.SourceRepairCatalog{}, bulletins.ErrConflict
	}
	// jsonb changes key order and whitespace. Recreate the writer's typed JSON
	// before checking its immutable snapshot digest.
	canonical, err := json.Marshal(snapshot)
	if err != nil {
		return onlinebulletins.SourceRepairCatalog{}, err
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != checksum || snapshot.Canonical != metadata || snapshot.Candidate.Evidence.SourceSHA256 != catalog.SourceChecksum {
		return onlinebulletins.SourceRepairCatalog{}, bulletins.ErrConflict
	}
	parts, err := onlinebulletins.CatalogParts(snapshot.Candidate.Evidence)
	if err != nil {
		return onlinebulletins.SourceRepairCatalog{}, err
	}
	for i := range parts {
		parts[i].Available = parts[i].Kind == "text" || snapshot.Candidate.SourceImages[parts[i].ID].AssetID != ""
	}
	catalog.Parts, catalog.Images = parts, snapshot.Candidate.SourceImages
	return catalog, nil
}

func (r *Repository) SaveOnlineBulletinSourceRepair(ctx context.Context, issueID string, repair onlinebulletins.PreparedSourceRepair, actor string, now time.Time) (onlinebulletins.SavedDraft, error) {
	return r.saveOnlineLocal(ctx, issueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: repair.Input.CanonicalVersion, OnlineVersion: repair.Input.OnlineVersion}, 0, nil, &repair, actor, now)
}
