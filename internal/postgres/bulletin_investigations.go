package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

const investigationColumns = `id::text,actor_id::text,issue_id::text,series,locale,revision,source_asset_key,source_sha256,renderer_version,model_sha256,scopes_json,evidence_sha256,evidence,status,COALESCE(result,''),COALESCE(receipt_id::text,''),COALESCE(error_code,''),COALESCE(lease_token::text,''),lease_until,attempts,COALESCE(candidate_set_hash,''),COALESCE(match_method,''),created_at,expires_at,evidence_expires_at`

var investigationStatusColumns = strings.Replace(investigationColumns, ",evidence,", ",NULL::bytea,", 1)

func scanInvestigation(row interface{ Scan(...any) error }) (v bulletinwatermark.Investigation, err error) {
	var lease sql.NullTime
	var scopes []byte
	err = row.Scan(&v.ID, &v.ActorID, &v.Scope.IssueID, &v.Scope.Series, &v.Scope.Locale, &v.Scope.Revision, &v.Scope.SourceAssetID, &v.Scope.SourceSHA256, &v.Scope.RendererVersion, &v.Scope.ModelSHA256, &scopes, &v.EvidenceSHA256, &v.Evidence, &v.Status, &v.Result, &v.ReceiptID, &v.ErrorCode, &v.LeaseToken, &lease, &v.Attempts, &v.CandidateSetHash, &v.MatchMethod, &v.CreatedAt, &v.ExpiresAt, &v.EvidenceExpiresAt)
	v.LeaseUntil = lease.Time
	if err == nil {
		err = json.Unmarshal(scopes, &v.Scopes)
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = bulletinwatermark.ErrNotFound
	}
	return
}

func lockWatermarkScope(ctx context.Context, tx *sql.Tx, scope bulletinwatermark.CandidateScope) error {
	if !validWatermarkAssetID(scope.SourceAssetID) {
		return bulletinwatermark.ErrInvalidFingerprint
	}
	asset, err := uuid.Parse(scope.SourceAssetID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,20260912))`, fmt.Sprintf("%s:%d:%s", asset.String(), scope.Revision, scope.RendererVersion))
	return err
}
func lockInvestigationSubjects(ctx context.Context, tx *sql.Tx, subjects []string) error {
	slices.Sort(subjects)
	for _, subject := range slices.Compact(subjects) {
		if err := lockWatermarkSubject(ctx, tx, subject); err != nil {
			return err
		}
	}
	return nil
}
func watermarkRestricted(ctx context.Context, tx *sql.Tx, actor string) (bool, error) {
	var blocked bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1)`, watermarkSubjectHash(actor)).Scan(&blocked)
	return blocked, err
}
func watermarkInvestigationRate(ctx context.Context, tx *sql.Tx, actor string, now time.Time) error {
	var count int
	err := tx.QueryRowContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_lookup_rate AS counters(subject_hash,window_start,count) VALUES($1,$2,1) ON CONFLICT(subject_hash,window_start) DO UPDATE SET count=counters.count+1 RETURNING count`, watermarkSubjectHash(actor), now.UTC().Truncate(time.Minute)).Scan(&count)
	if err != nil {
		return err
	}
	if count > 10 {
		return bulletinwatermark.ErrRateLimited
	}
	return nil
}

func (r *Repository) CreateWatermarkInvestigation(ctx context.Context, v bulletinwatermark.Investigation) error {
	hash := func(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }
	scopes := v.CandidateScopes()
	if len(scopes) == 0 || len(scopes) > 1000 || uuid.Validate(v.ID) != nil || uuid.Validate(v.ActorID) != nil || len(v.Evidence) < 1 || len(v.Evidence) > bulletinwatermark.MaxStoredEvidenceBytes || v.CreatedAt.IsZero() {
		return bulletinwatermark.ErrInvalidInvestigation
	}
	for index := range scopes {
		scopes[index].Series = bulletinwatermark.NormalizeSeries(scopes[index].Series)
		scope := scopes[index]
		if uuid.Validate(scope.IssueID) != nil || !validWatermarkAssetID(scope.SourceAssetID) || !bulletinwatermark.ValidEdition(scope.Series, scope.Locale) || scope.Revision < 1 || scope.RendererVersion == "" || !hash(scope.SourceSHA256) || !hash(scope.ModelSHA256) || scope.IssueID != scopes[0].IssueID || scope.Series != scopes[0].Series {
			return bulletinwatermark.ErrInvalidInvestigation
		}
	}
	v.Scope, v.Scopes = scopes[0], scopes
	scopesJSON, _ := json.Marshal(scopes)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	idKey, _ := uuid.Parse(v.ID)
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,20260913))`, idKey.String()); err != nil {
		return err
	}
	if err = lockWatermarkSubject(ctx, tx, v.ActorID); err != nil {
		return err
	}
	blocked, err := watermarkRestricted(ctx, tx, v.ActorID)
	if err != nil {
		return err
	}
	if blocked {
		return bulletinwatermark.ErrRestricted
	}
	digest := sha256.Sum256(v.Evidence)
	v.EvidenceSHA256 = hex.EncodeToString(digest[:])
	existing, readErr := scanInvestigation(tx.QueryRowContext(ctx, `SELECT `+investigationStatusColumns+` FROM hhc_web.bulletin_watermark_investigation WHERE id=$1`, v.ID))
	if readErr == nil {
		if existing.ActorID != v.ActorID || !slices.Equal(existing.CandidateScopes(), scopes) || existing.EvidenceSHA256 != v.EvidenceSHA256 {
			return bulletinwatermark.ErrInvestigationConflict
		}
		return tx.Commit()
	}
	if !errors.Is(readErr, bulletinwatermark.ErrNotFound) {
		return readErr
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.bulletin_watermark_investigation WHERE actor_id=$1 AND status IN ('queued','running') AND evidence_expires_at>$2`, v.ActorID, v.CreatedAt).Scan(&pending); err != nil {
		return err
	}
	if pending >= 3 {
		return bulletinwatermark.ErrRateLimited
	}
	if err = watermarkInvestigationRate(ctx, tx, v.ActorID, v.CreatedAt); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_investigation(id,actor_id,issue_id,series,locale,revision,source_asset_id,source_sha256,renderer_version,model_sha256,scopes_json,evidence_sha256,evidence,status,created_at,expires_at,evidence_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'queued',$14,$15,$16)`, v.ID, v.ActorID, v.Scope.IssueID, v.Scope.Series, v.Scope.Locale, v.Scope.Revision, v.Scope.SourceAssetID, v.Scope.SourceSHA256, v.Scope.RendererVersion, v.Scope.ModelSHA256, scopesJSON, v.EvidenceSHA256, v.Evidence, v.CreatedAt, v.CreatedAt.Add(bulletinwatermark.Retention), v.CreatedAt.Add(bulletinwatermark.InvestigationEvidenceTTL))
	if err != nil {
		return err
	}
	if err = insertWatermarkQueueEvent(ctx, tx, "investigation", v.ID, 1, v.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ClaimWatermarkInvestigationByID(ctx context.Context, id string, now time.Time) (v bulletinwatermark.Investigation, err error) {
	if uuid.Validate(id) != nil {
		return v, bulletinwatermark.ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	v, err = scanInvestigation(tx.QueryRowContext(ctx, `SELECT `+investigationColumns+` FROM hhc_web.bulletin_watermark_investigation WHERE id=$2 AND evidence_expires_at>$1 AND (status='queued' OR (status='running' AND lease_until<=$1 AND attempts<3)) FOR UPDATE`, now, id))
	if err != nil {
		return v, err
	}
	v.Status, v.LeaseToken, v.LeaseUntil = "running", uuid.NewString(), now.Add(bulletinwatermark.InvestigationLease)
	v.Attempts++
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_watermark_investigation SET status='running',lease_token=$2,lease_until=$3,attempts=attempts+1 WHERE id=$1`, v.ID, v.LeaseToken, v.LeaseUntil); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (r *Repository) ExhaustWatermarkInvestigation(ctx context.Context, id string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.bulletin_watermark_investigation SET status='failed',error_code='retry_exhausted',evidence=NULL,lease_token=NULL,lease_until=NULL WHERE id=$1 AND status IN ('queued','running') AND expires_at>$2`, id, now)
	return err
}

func (r *Repository) ClaimWatermarkInvestigation(ctx context.Context, now time.Time) (v bulletinwatermark.Investigation, found bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return v, false, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_watermark_investigation SET status=CASE WHEN evidence_expires_at<=$1 THEN 'expired' ELSE 'failed' END,error_code=CASE WHEN evidence_expires_at<=$1 THEN NULL ELSE 'job_timeout' END,evidence=NULL,lease_token=NULL,lease_until=NULL WHERE id IN (SELECT id FROM hhc_web.bulletin_watermark_investigation WHERE status IN ('queued','running') AND (evidence_expires_at<=$1 OR (status='running' AND lease_until<=$1 AND attempts>=2)) ORDER BY created_at LIMIT 1000 FOR UPDATE SKIP LOCKED)`, now)
	if err != nil {
		return v, false, err
	}
	v, err = scanInvestigation(tx.QueryRowContext(ctx, `SELECT `+investigationColumns+` FROM hhc_web.bulletin_watermark_investigation WHERE evidence_expires_at>$1 AND (status='queued' OR (status='running' AND lease_until<=$1 AND attempts<2)) ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
	if errors.Is(err, bulletinwatermark.ErrNotFound) {
		return v, false, tx.Commit()
	}
	if err != nil {
		return v, false, err
	}
	v.Status = "running"
	v.LeaseToken = uuid.NewString()
	v.LeaseUntil = now.Add(bulletinwatermark.InvestigationLease)
	v.Attempts++
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_watermark_investigation SET status='running',lease_token=$2,lease_until=$3,attempts=attempts+1 WHERE id=$1`, v.ID, v.LeaseToken, v.LeaseUntil)
	if err != nil {
		return v, false, err
	}
	return v, true, tx.Commit()
}

func (r *Repository) CompleteWatermarkInvestigation(ctx context.Context, id, lease string, completion bulletinwatermark.InvestigationCompletion, now time.Time) error {
	outcome, receiptID, candidateSetHash, errorCode := completion.Outcome, completion.ReceiptID, completion.CandidateSetHash, completion.ReasonCode
	if uuid.Validate(id) != nil || uuid.Validate(lease) != nil || !slices.Contains([]string{"matched", "inconclusive", "conflict", "failed"}, outcome) {
		return bulletinwatermark.ErrInvalidInvestigation
	}
	validMethod := slices.Contains([]bulletinwatermark.MatchMethod{bulletinwatermark.MatchOutputSHA256, bulletinwatermark.MatchPayload, bulletinwatermark.MatchCandidate}, completion.MatchMethod)
	if outcome == "matched" && (uuid.Validate(receiptID) != nil || !validMethod) || outcome != "matched" && (receiptID != "" || completion.MatchMethod != "") {
		return bulletinwatermark.ErrInvalidInvestigation
	}
	validHash := func(value string) bool { return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == "" }
	if candidateSetHash != "" && !validHash(candidateSetHash) {
		return bulletinwatermark.ErrInvalidInvestigation
	}
	if outcome == "matched" {
		switch completion.MatchMethod {
		case bulletinwatermark.MatchOutputSHA256:
			if !validHash(completion.Proof) || candidateSetHash != "" {
				return bulletinwatermark.ErrInvalidInvestigation
			}
		case bulletinwatermark.MatchPayload:
			if len(completion.Proof) != 61 || strings.Trim(completion.Proof, "01") != "" || candidateSetHash != "" {
				return bulletinwatermark.ErrInvalidInvestigation
			}
		case bulletinwatermark.MatchCandidate:
			if completion.Proof == "" {
				completion.Proof = candidateSetHash
			}
			if !validHash(completion.Proof) || completion.Proof != candidateSetHash {
				return bulletinwatermark.ErrInvalidInvestigation
			}
		}
	} else if completion.Proof != "" {
		return bulletinwatermark.ErrInvalidInvestigation
	}
	invalidReason := len(errorCode) > 80 || strings.Trim(errorCode, "abcdefghijklmnopqrstuvwxyz_") != ""
	if invalidReason || outcome == "failed" && errorCode == "" {
		return bulletinwatermark.ErrInvalidInvestigation
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Discover lock keys without row locks. DSR owns subject then row order.
	v, err := scanInvestigation(tx.QueryRowContext(ctx, `SELECT `+investigationStatusColumns+` FROM hhc_web.bulletin_watermark_investigation WHERE id=$1`, id))
	if err != nil {
		return err
	}
	var discovered bulletinwatermark.Receipt
	if outcome == "matched" {
		discovered, err = scanWatermark(tx.QueryRowContext(ctx, `SELECT `+watermarkColumns+` FROM hhc_web.bulletin_watermark_receipt WHERE id=$1`, receiptID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	lockScopes := append([]bulletinwatermark.CandidateScope(nil), v.CandidateScopes()...)
	if discovered.ID != "" {
		lockScopes = append(lockScopes, bulletinwatermark.CandidateScope{IssueID: discovered.IssueID, Series: discovered.Series, Locale: discovered.Locale, Revision: discovered.Revision, SourceAssetID: discovered.SourceAssetID, SourceSHA256: discovered.SourceSHA256, RendererVersion: discovered.RendererVersion, ModelSHA256: discovered.ModelSHA256})
	}
	slices.SortFunc(lockScopes, func(a, b bulletinwatermark.CandidateScope) int {
		return strings.Compare(fmt.Sprintf("%s:%020d:%s", a.SourceAssetID, a.Revision, a.RendererVersion), fmt.Sprintf("%s:%020d:%s", b.SourceAssetID, b.Revision, b.RendererVersion))
	})
	for _, scope := range slices.Compact(lockScopes) {
		if err = lockWatermarkScope(ctx, tx, scope); err != nil {
			return err
		}
	}
	subjects := []string{v.ActorID}
	var candidates []bulletinwatermark.Receipt
	if discovered.UserID != "" {
		subjects = append(subjects, discovered.UserID)
	}
	if outcome != "failed" && (completion.MatchMethod == bulletinwatermark.MatchCandidate || candidateSetHash != "") {
		scopes := v.CandidateScopes()
		if completion.MatchMethod == bulletinwatermark.MatchCandidate && discovered.ID != "" {
			scope := bulletinwatermark.CandidateScope{IssueID: discovered.IssueID, Series: discovered.Series, Locale: discovered.Locale, Revision: discovered.Revision, SourceAssetID: discovered.SourceAssetID, SourceSHA256: discovered.SourceSHA256, RendererVersion: discovered.RendererVersion, ModelSHA256: discovered.ModelSHA256}
			if slices.Contains(scopes, scope) {
				scopes = []bulletinwatermark.CandidateScope{scope}
			}
		}
		for _, scope := range scopes {
			set, listErr := listWatermarkCandidates(ctx, tx, scope, now, 64)
			if errors.Is(listErr, bulletinwatermark.ErrCandidateLimit) {
				outcome, errorCode, receiptID = "failed", "candidate_limit", ""
				break
			} else if listErr != nil {
				return listErr
			}
			candidates = append(candidates, set...)
		}
		for _, candidate := range candidates {
			subjects = append(subjects, candidate.UserID)
		}
	}
	if err = lockInvestigationSubjects(ctx, tx, subjects); err != nil {
		return err
	}
	v, err = scanInvestigation(tx.QueryRowContext(ctx, `SELECT `+investigationStatusColumns+` FROM hhc_web.bulletin_watermark_investigation WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if v.Status != "running" || v.LeaseToken != lease || !v.LeaseUntil.After(now) || !v.EvidenceExpiresAt.After(now) {
		return bulletinwatermark.ErrInvestigationLease
	}
	blocked, err := watermarkRestricted(ctx, tx, v.ActorID)
	if err != nil {
		return err
	}
	if blocked {
		outcome = "failed"
		errorCode = "processing_restricted"
		receiptID = ""
		completion.MatchMethod = ""
	}
	if outcome == "matched" && completion.MatchMethod != bulletinwatermark.MatchCandidate {
		receipt, readErr := bulletinwatermark.Receipt{}, sql.ErrNoRows
		if discovered.ID != "" {
			receipt, readErr = scanWatermark(tx.QueryRowContext(ctx, `SELECT `+watermarkColumns+` FROM hhc_web.bulletin_watermark_receipt WHERE id=$1 AND expires_at>$2 FOR SHARE`, receiptID, now))
		}
		restricted := false
		if readErr == nil {
			restricted, err = watermarkRestricted(ctx, tx, receipt.UserID)
			if err != nil {
				return err
			}
		}
		proofMatches := readErr == nil && !restricted && (completion.MatchMethod == bulletinwatermark.MatchOutputSHA256 && receipt.OutputSHA256 == completion.Proof || completion.MatchMethod == bulletinwatermark.MatchPayload && receipt.FingerprintPayload == completion.Proof && receipt.ValidateFingerprint() == nil)
		if !proofMatches {
			outcome, errorCode, receiptID, completion.MatchMethod = "inconclusive", "no_active_issuance", "", ""
		}
	} else if outcome != "failed" && (completion.MatchMethod == bulletinwatermark.MatchCandidate || candidateSetHash != "") {
		fresh := []bulletinwatermark.Receipt{}
		scopes := v.CandidateScopes()
		if completion.MatchMethod == bulletinwatermark.MatchCandidate && discovered.ID != "" {
			scope := bulletinwatermark.CandidateScope{IssueID: discovered.IssueID, Series: discovered.Series, Locale: discovered.Locale, Revision: discovered.Revision, SourceAssetID: discovered.SourceAssetID, SourceSHA256: discovered.SourceSHA256, RendererVersion: discovered.RendererVersion, ModelSHA256: discovered.ModelSHA256}
			if slices.Contains(scopes, scope) {
				scopes = []bulletinwatermark.CandidateScope{scope}
			}
		}
		for _, scope := range scopes {
			set, listErr := listWatermarkCandidates(ctx, tx, scope, now, 64)
			if listErr != nil {
				return listErr
			}
			fresh = append(fresh, set...)
		}
		matched := outcome != "matched"
		for _, candidate := range fresh {
			matched = matched || candidate.ID == receiptID
		}
		if len(fresh) < 1 || bulletinwatermark.CandidateSetHash(fresh) != candidateSetHash || !matched {
			outcome, errorCode, receiptID, completion.MatchMethod = "failed", "candidate_set_changed", "", ""
		}
	}
	if outcome != "failed" && candidateSetHash == "" {
		// The original terminal-state constraint requires a structural snapshot;
		// exact methods have no candidate set and never persist their proof.
		candidateSetHash = bulletinwatermark.CandidateSetHash(nil)
	}
	status, result := "completed", outcome
	if outcome == "failed" {
		status = "failed"
		result = ""
		candidateSetHash = ""
		completion.MatchMethod = ""
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_watermark_investigation SET status=$2,result=NULLIF($3,''),receipt_id=NULLIF($4,'')::uuid,error_code=NULLIF($5,''),candidate_set_hash=NULLIF($6,''),match_method=NULLIF($7,''),evidence=NULL,lease_token=NULL,lease_until=NULL WHERE id=$1`, id, status, result, receiptID, errorCode, candidateSetHash, completion.MatchMethod)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) GetWatermarkInvestigation(ctx context.Context, id, actor string, now time.Time) (view bulletinwatermark.InvestigationView, err error) {
	if uuid.Validate(id) != nil || uuid.Validate(actor) != nil {
		return view, bulletinwatermark.ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	v, err := scanInvestigation(tx.QueryRowContext(ctx, `SELECT `+investigationStatusColumns+` FROM hhc_web.bulletin_watermark_investigation WHERE id=$1 AND actor_id=$2 AND expires_at>$3`, id, actor, now))
	if err != nil {
		return view, err
	}
	subjects := []string{actor}
	if v.ReceiptID != "" {
		var subject string
		err = tx.QueryRowContext(ctx, `SELECT user_id::text FROM hhc_web.bulletin_watermark_receipt WHERE id=$1`, v.ReceiptID).Scan(&subject)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return view, err
		}
		if subject != "" {
			subjects = append(subjects, subject)
		}
	}
	if err = lockInvestigationSubjects(ctx, tx, subjects); err != nil {
		return view, err
	}
	blocked, err := watermarkRestricted(ctx, tx, actor)
	if err != nil {
		return view, err
	}
	if blocked {
		return view, bulletinwatermark.ErrRestricted
	}
	v, err = scanInvestigation(tx.QueryRowContext(ctx, `SELECT `+investigationStatusColumns+` FROM hhc_web.bulletin_watermark_investigation WHERE id=$1 AND actor_id=$2 AND expires_at>$3 FOR UPDATE`, id, actor, now))
	if err != nil {
		return view, err
	}
	if (v.Status == "queued" || v.Status == "running") && !v.EvidenceExpiresAt.After(now) {
		_, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_watermark_investigation SET status='expired',evidence=NULL,lease_token=NULL,lease_until=NULL WHERE id=$1`, id)
		if err != nil {
			return view, err
		}
		v.Status = "expired"
	}
	// A completion racing discovery can add a new subject; retry rather than lock out of order.
	if v.Status == "completed" && v.Result == "matched" && v.ReceiptID != "" {
		receipt, readErr := scanWatermark(tx.QueryRowContext(ctx, `SELECT `+watermarkColumns+` FROM hhc_web.bulletin_watermark_receipt WHERE id=$1 AND expires_at>$2`, v.ReceiptID, now))
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return view, readErr
		}
		if readErr == nil && !slices.Contains(subjects, receipt.UserID) {
			return view, bulletinwatermark.ErrInvestigationLease
		}
		restricted := false
		if readErr == nil {
			restricted, err = watermarkRestricted(ctx, tx, receipt.UserID)
			if err != nil {
				return view, err
			}
		}
		scope := bulletinwatermark.CandidateScope{IssueID: receipt.IssueID, Series: receipt.Series, Locale: receipt.Locale, Revision: receipt.Revision, SourceAssetID: receipt.SourceAssetID, SourceSHA256: receipt.SourceSHA256, RendererVersion: receipt.RendererVersion, ModelSHA256: receipt.ModelSHA256}
		if readErr == nil && !restricted && (v.MatchMethod != bulletinwatermark.MatchCandidate || slices.Contains(v.CandidateScopes(), scope)) {
			if err = watermarkInvestigationRate(ctx, tx, actor, now); err != nil {
				return view, err
			}
			payload, _ := json.Marshal(map[string]any{"matched": true, "investigationId": v.ID})
			_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.cms_audit_event(id,action,resource_type,resource_id,actor,payload_json,created_at) VALUES($1,'bulletin_watermark.lookup','bulletin_watermark_receipt',$2,$3,$4,$5)`, uuid.NewString(), receipt.ID, actor, payload, now)
			if err != nil {
				return view, err
			}
			view.Receipt = &receipt
		} else {
			v.Result = "inconclusive"
			v.ReceiptID = ""
		}
	} else if v.Status == "completed" && v.Result == "matched" {
		v.Result = "inconclusive"
	}
	v.Evidence = nil
	v.LeaseToken = ""
	v.CandidateSetHash = ""
	view.Investigation = v
	if err = tx.Commit(); err != nil {
		return bulletinwatermark.InvestigationView{}, err
	}
	return view, nil
}

func (r *Repository) ListWatermarkVersions(ctx context.Context, issueID string, now time.Time) ([]bulletinwatermark.CandidateVersion, error) {
	if uuid.Validate(issueID) != nil {
		return nil, bulletinwatermark.ErrInvalidInvestigation
	}
	rows, err := r.db.QueryContext(ctx, `SELECT issue_id::text,series,locale,revision,source_asset_key,source_sha256,renderer_version,model_sha256,min(jsonb_array_length(pages_json)),max(jsonb_array_length(pages_json)) FROM hhc_web.bulletin_watermark_receipt WHERE issue_id=$1 AND fingerprint_payload IS NOT NULL AND expires_at>$2 AND NOT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject s WHERE s.subject_hash=encode(sha256(convert_to('bulletin-watermark-subject:'||user_id::text,'UTF8')),'hex')) GROUP BY issue_id,series,locale,revision,source_asset_id,source_asset_key,source_sha256,renderer_version,model_sha256 ORDER BY revision DESC,series,locale,source_asset_id LIMIT 1001`, issueID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bulletinwatermark.CandidateVersion{}
	for rows.Next() {
		var v bulletinwatermark.CandidateVersion
		var maxPages int
		if err = rows.Scan(&v.IssueID, &v.Series, &v.Locale, &v.Revision, &v.SourceAssetID, &v.SourceSHA256, &v.RendererVersion, &v.ModelSHA256, &v.PageCount, &maxPages); err != nil {
			return nil, err
		}
		if v.PageCount != maxPages || v.PageCount < 1 || v.PageCount > 60 {
			return nil, bulletinwatermark.ErrInvalidFingerprint
		}
		out = append(out, v)
		if len(out) > 1000 {
			return nil, bulletinwatermark.ErrCandidateLimit
		}
	}
	return out, rows.Err()
}

func (r *Repository) ListWatermarkInvestigations(ctx context.Context, issueID, actor, cursor string, limit int, now time.Time) (bulletinwatermark.InvestigationHistoryPage, error) {
	if uuid.Validate(issueID) != nil || uuid.Validate(actor) != nil || cursor != "" && uuid.Validate(cursor) != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, bulletinwatermark.ErrInvalidInvestigation
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	defer tx.Rollback()
	if err = lockWatermarkSubject(ctx, tx, actor); err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	blocked, err := watermarkRestricted(ctx, tx, actor)
	if err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	if blocked {
		return bulletinwatermark.InvestigationHistoryPage{}, bulletinwatermark.ErrRestricted
	}
	rows, err := tx.QueryContext(ctx, `SELECT i.id::text,i.actor_id::text,i.issue_id::text,COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN r.issue_id::text END,''),COALESCE(r.locale,i.locale),COALESCE(r.revision,i.revision),i.status,COALESCE(i.result,''),COALESCE(i.error_code,''),COALESCE(i.match_method,''),i.created_at,COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN r.user_id::text END,''),CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN r.issued_at END FROM hhc_web.bulletin_watermark_investigation i LEFT JOIN hhc_web.bulletin_watermark_receipt r ON r.id=i.receipt_id AND r.expires_at>$4 LEFT JOIN hhc_web.bulletin_watermark_subject s ON s.subject_hash=encode(sha256(convert_to('bulletin-watermark-subject:'||r.user_id::text,'UTF8')),'hex') WHERE i.issue_id=$1 AND i.expires_at>$4 AND ($2='' OR (i.created_at,i.id)<(SELECT created_at,id FROM hhc_web.bulletin_watermark_investigation WHERE id=NULLIF($2,'')::uuid)) ORDER BY i.created_at DESC,i.id DESC LIMIT $3`, issueID, cursor, limit+1, now)
	if err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	defer rows.Close()
	page := bulletinwatermark.InvestigationHistoryPage{Items: []bulletinwatermark.InvestigationHistoryRow{}}
	for rows.Next() {
		var item bulletinwatermark.InvestigationHistoryRow
		var issuedAt sql.NullTime
		if err = rows.Scan(&item.ID, &item.ActorID, &item.IssueID, &item.ActualIssueID, &item.Locale, &item.Revision, &item.Status, &item.Result, &item.ErrorCode, &item.MatchMethod, &item.CreatedAt, &item.MatchedAccountID, &issuedAt); err != nil {
			return bulletinwatermark.InvestigationHistoryPage{}, err
		}
		item.IssuedAt = issuedAt.Time
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	if len(page.Items) > limit {
		page.NextCursor = page.Items[limit-1].ID
		page.Items = page.Items[:limit]
	}
	if err = tx.Commit(); err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	return page, nil
}

type globalInvestigationCursor struct {
	Sort, Direction, Key, ID string
	Missing                  int
}

func (r *Repository) ListGlobalWatermarkInvestigations(ctx context.Context, actor, cursor string, limit int, sortKey, direction string, now time.Time) (bulletinwatermark.InvestigationHistoryPage, error) {
	if uuid.Validate(actor) != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, bulletinwatermark.ErrInvalidInvestigation
	}
	decoded, err := decodeGlobalInvestigationCursor(cursor)
	if err != nil || decoded != nil && (decoded.Sort != sortKey || decoded.Direction != direction) {
		return bulletinwatermark.InvestigationHistoryPage{}, bulletinwatermark.ErrInvalidInvestigation
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	defer tx.Rollback()
	keyExpression, missingExpression, ok := globalInvestigationSort(sortKey)
	if !ok || direction != "asc" && direction != "desc" {
		return bulletinwatermark.InvestigationHistoryPage{}, bulletinwatermark.ErrInvalidInvestigation
	}
	comparison := ">"
	if direction == "desc" {
		comparison = "<"
	}
	args := []any{now, limit + 1}
	cursorWhere := ""
	if decoded != nil {
		args = append(args, decoded.Missing, decoded.Key, decoded.ID)
		cursorWhere = fmt.Sprintf(` AND ((%s)>$3 OR ((%s)=$3 AND ((%s)%s$4 OR ((%s)=$4 AND i.id%s$5::uuid))))`, missingExpression, missingExpression, keyExpression, comparison, keyExpression, comparison)
	}
	query := fmt.Sprintf(`SELECT i.id::text,COALESCE(CASE WHEN s.subject_hash IS NULL THEN r.user_id::text END,''),(%s)::text,(%s) FROM hhc_web.bulletin_watermark_investigation i JOIN hhc_web.bulletin_issue requested ON requested.id=i.issue_id LEFT JOIN hhc_web.bulletin_watermark_receipt r ON r.id=i.receipt_id AND r.expires_at>$1 LEFT JOIN hhc_web.bulletin_issue actual ON actual.id=r.issue_id LEFT JOIN hhc_web.bulletin_watermark_subject s ON s.subject_hash=encode(sha256(convert_to('bulletin-watermark-subject:'||r.user_id::text,'UTF8')),'hex') LEFT JOIN hhc_web.account_identity_projection actorp ON actorp.account_user_id=i.actor_id LEFT JOIN hhc_web.account_identity_projection matchedp ON matchedp.account_user_id=r.user_id WHERE i.expires_at>$1%s ORDER BY (%s) ASC,(%s) %s,i.id %s LIMIT $2`, keyExpression, missingExpression, cursorWhere, missingExpression, keyExpression, direction, direction)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	subjects := []string{actor}
	ids := []string{}
	cursors := []globalInvestigationCursor{}
	for rows.Next() {
		var id, subject, key string
		var missing int
		if err = rows.Scan(&id, &subject, &key, &missing); err != nil {
			rows.Close()
			return bulletinwatermark.InvestigationHistoryPage{}, err
		}
		ids = append(ids, id)
		cursors = append(cursors, globalInvestigationCursor{Sort: sortKey, Direction: direction, Key: key, ID: id, Missing: missing})
		if subject != "" {
			subjects = append(subjects, subject)
		}
	}
	if err = rows.Close(); err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	if err = lockInvestigationSubjects(ctx, tx, subjects); err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	blocked, err := watermarkRestricted(ctx, tx, actor)
	if err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	if blocked {
		return bulletinwatermark.InvestigationHistoryPage{}, bulletinwatermark.ErrRestricted
	}
	nextCursor := ""
	if len(ids) > limit {
		nextCursor = encodeGlobalInvestigationCursor(cursors[limit-1])
		ids = ids[:limit]
	}
	if len(ids) == 0 {
		return bulletinwatermark.InvestigationHistoryPage{Items: []bulletinwatermark.InvestigationHistoryRow{}}, tx.Commit()
	}
	args, placeholders := []any{now}, make([]string, len(ids))
	for index, id := range ids {
		args = append(args, id)
		placeholders[index] = fmt.Sprintf("$%d::uuid", index+2)
	}
	query = `SELECT i.id::text,i.actor_id::text,COALESCE(actorp.display_name,''),COALESCE(actorp.email,''),COALESCE(actorp.available,false),i.issue_id::text,COALESCE(requested.issue_number,0),requested.issue_date::text,COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN r.issue_id::text END,''),COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN actual.issue_number END,0),COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN actual.issue_date::text END,''),COALESCE(r.locale,i.locale),COALESCE(r.revision,i.revision),i.status,COALESCE(i.result,''),COALESCE(i.error_code,''),COALESCE(i.match_method,''),i.created_at,COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN r.user_id::text END,''),COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN matchedp.display_name END,''),COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN matchedp.email END,''),COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN matchedp.available END,false),COALESCE(CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN r.id::text END,''),CASE WHEN i.status='completed' AND i.result='matched' AND s.subject_hash IS NULL THEN r.issued_at END FROM hhc_web.bulletin_watermark_investigation i JOIN hhc_web.bulletin_issue requested ON requested.id=i.issue_id LEFT JOIN hhc_web.bulletin_watermark_receipt r ON r.id=i.receipt_id AND r.expires_at>$1 LEFT JOIN hhc_web.bulletin_issue actual ON actual.id=r.issue_id LEFT JOIN hhc_web.bulletin_watermark_subject s ON s.subject_hash=encode(sha256(convert_to('bulletin-watermark-subject:'||r.user_id::text,'UTF8')),'hex') LEFT JOIN hhc_web.account_identity_projection actorp ON actorp.account_user_id=i.actor_id LEFT JOIN hhc_web.account_identity_projection matchedp ON matchedp.account_user_id=r.user_id WHERE i.id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err = tx.QueryContext(ctx, query, args...)
	if err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	page := bulletinwatermark.InvestigationHistoryPage{Items: []bulletinwatermark.InvestigationHistoryRow{}}
	for rows.Next() {
		var item bulletinwatermark.InvestigationHistoryRow
		var issuedAt sql.NullTime
		if err = rows.Scan(&item.ID, &item.ActorID, &item.ActorDisplayName, &item.ActorEmail, &item.ActorAvailable, &item.IssueID, &item.IssueNumber, &item.IssueDate, &item.ActualIssueID, &item.ActualIssueNumber, &item.ActualIssueDate, &item.Locale, &item.Revision, &item.Status, &item.Result, &item.ErrorCode, &item.MatchMethod, &item.CreatedAt, &item.MatchedAccountID, &item.MatchedDisplayName, &item.MatchedEmail, &item.MatchedAvailable, &item.ReceiptID, &issuedAt); err != nil {
			rows.Close()
			return bulletinwatermark.InvestigationHistoryPage{}, err
		}
		item.IssuedAt = issuedAt.Time
		page.Items = append(page.Items, item)
	}
	if err = rows.Close(); err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	order := make(map[string]int, len(ids))
	for index, id := range ids {
		order[id] = index
	}
	sort.SliceStable(page.Items, func(i, j int) bool { return order[page.Items[i].ID] < order[page.Items[j].ID] })
	page.NextCursor = nextCursor
	audited := false
	for _, item := range page.Items {
		if item.ReceiptID == "" {
			continue
		}
		if !audited {
			if err = watermarkInvestigationRate(ctx, tx, actor, now); err != nil {
				return bulletinwatermark.InvestigationHistoryPage{}, err
			}
			audited = true
		}
		payload, _ := json.Marshal(map[string]any{"matched": true, "investigationId": item.ID})
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.cms_audit_event(id,action,resource_type,resource_id,actor,payload_json,created_at) VALUES($1,'bulletin_watermark.lookup','bulletin_watermark_receipt',$2,$3,$4,$5)`, uuid.NewString(), item.ReceiptID, actor, payload, now); err != nil {
			return bulletinwatermark.InvestigationHistoryPage{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return bulletinwatermark.InvestigationHistoryPage{}, err
	}
	return page, nil
}

func globalInvestigationSort(key string) (string, string, bool) {
	switch key {
	case "submitted":
		return `to_char(i.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US')`, "0::int", true
	case "requested":
		return `lpad(COALESCE(requested.issue_number,0)::text,20,'0')||':'||requested.issue_date::text`, "0::int", true
	case "actual":
		return `COALESCE(lpad(actual.issue_number::text,20,'0')||':'||actual.issue_date::text,'')`, `CASE WHEN actual.id IS NULL OR s.subject_hash IS NOT NULL THEN 1 ELSE 0 END`, true
	case "status":
		return `i.status||':'||COALESCE(i.result,'')`, "0::int", true
	case "operator":
		return `lower(COALESCE(NULLIF(actorp.display_name,''),NULLIF(actorp.email,''),i.actor_id::text))`, `CASE WHEN COALESCE(actorp.available,false) THEN 0 ELSE 1 END`, true
	case "account":
		return `lower(COALESCE(NULLIF(matchedp.display_name,''),NULLIF(matchedp.email,''),r.user_id::text,''))`, `CASE WHEN r.user_id IS NOT NULL AND s.subject_hash IS NULL AND COALESCE(matchedp.available,false) THEN 0 ELSE 1 END`, true
	case "issued":
		return `COALESCE(to_char(r.issued_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US'),'')`, `CASE WHEN r.issued_at IS NULL OR s.subject_hash IS NOT NULL THEN 1 ELSE 0 END`, true
	default:
		return "", "", false
	}
}

func encodeGlobalInvestigationCursor(cursor globalInvestigationCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeGlobalInvestigationCursor(value string) (*globalInvestigationCursor, error) {
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	var cursor globalInvestigationCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, err
	}
	if uuid.Validate(cursor.ID) != nil || cursor.Key == "" && cursor.Missing == 0 || cursor.Missing < 0 || cursor.Missing > 1 {
		return nil, errors.New("invalid investigation cursor")
	}
	return &cursor, nil
}
