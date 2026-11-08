package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func captureConfirmedBodyKinds(ctx context.Context, tx *sql.Tx, confirmed onlinebulletins.ConfirmedRevision, local onlinebulletins.Document, canonical onlinebulletins.CanonicalMetadata, actor string, now time.Time) error {
	if canonical.IssueNumber < 1739 || local.ContentLocale != "zh-Hant" || local.Series != "general" {
		return nil
	}
	var jobID string
	var extractedRevision int64
	var snapshotJSON, baseJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT j.id::text,j.result_revision,j.snapshot_json,r.document_json
	 FROM hhc_web.online_bulletin_extraction_job j JOIN hhc_web.online_bulletin_revision r ON r.document_id=j.document_id AND r.revision=j.result_revision
	 WHERE j.document_id=$1 AND j.source_asset_checksum=$2 AND j.result_revision<$3 AND j.snapshot_json IS NOT NULL
	 ORDER BY j.result_revision DESC LIMIT 1`, confirmed.DocumentID, local.SourceAssetChecksum, confirmed.Revision).Scan(&jobID, &extractedRevision, &snapshotJSON, &baseJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var corrections []onlinebulletins.BodyKindCorrection
	if err == nil {
		var base onlinebulletins.Document
		var snapshot struct {
			Candidate onlinebulletins.DocumentCandidate `json:"candidate"`
		}
		if json.Unmarshal(baseJSON, &base) != nil || json.Unmarshal(snapshotJSON, &snapshot) != nil {
			return bulletins.ErrConflict
		}
		corrections = onlinebulletins.CollectBodyKindCorrections(base, local, snapshot.Candidate.Evidence)
		reversed := 0
		for _, component := range local.Components {
			for _, application := range snapshot.Candidate.LearningApplications {
				if component.BodySection != nil && application.ComponentID == component.ID && application.Kind != component.BodySection.Kind {
					reversed++
				}
			}
		}
		slog.InfoContext(ctx, "bulletin classification evidence evaluated", "document_id", confirmed.DocumentID, "revision", confirmed.Revision, "human_corrections", len(corrections), "automatic_reversals", reversed)
	}
	// Every newer confirmation supersedes the document's earlier learning vote,
	// including a return to the extraction baseline or an ineligible mixed edit.
	// Immutable confirmations provide the withdrawal link; no historical evidence
	// is deleted and a delayed old event cannot regain a vote.
	signatures := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT signature FROM hhc_web.online_bulletin_learning_example WHERE document_id=$1`, confirmed.DocumentID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var signature string
		if err = rows.Scan(&signature); err != nil {
			rows.Close()
			return err
		}
		signatures[signature] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range corrections {
		signatures[c.Signature] = true
	}
	ordered := make([]string, 0, len(signatures))
	for signature := range signatures {
		ordered = append(ordered, signature)
	}
	sort.Strings(ordered)
	for _, signature := range ordered {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1739))`, signature); err != nil {
			return err
		}
	}
	for _, correction := range corrections {
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_learning_rule(signature,version,status,kind,title,speaker) VALUES($1,1,'pending',$2,$3,$4) ON CONFLICT DO NOTHING`, correction.Signature, correction.Kind, correction.Title, correction.Speaker); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_learning_rule_revision(signature,version,status,kind,created_by,created_at) SELECT signature,version,status,kind,$2,$3 FROM hhc_web.online_bulletin_learning_rule WHERE signature=$1 ON CONFLICT DO NOTHING`, correction.Signature, actor, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_learning_example(signature,issue_id,document_id,revision,extracted_revision,extraction_job_id,component_id,source_page,source_checksum,layout_validation_hash,kind,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`, correction.Signature, local.IssueID, confirmed.DocumentID, confirmed.Revision, extractedRevision, jobID, correction.ComponentID, correction.SourcePage, local.SourceAssetChecksum, local.LayoutManifest.LayoutValidationHash, correction.Kind, actor, now); err != nil {
			return err
		}
	}
	for _, signature := range ordered {
		var version int64
		var status, kind string
		if err = tx.QueryRowContext(ctx, `SELECT version,status,kind FROM hhc_web.online_bulletin_learning_rule WHERE signature=$1 FOR UPDATE`, signature).Scan(&version, &status, &kind); err != nil {
			return err
		}
		var issues, kinds int
		var effectiveKind string
		if err = tx.QueryRowContext(ctx, `SELECT count(DISTINCT issue_id),count(DISTINCT kind),COALESCE(min(kind),'') FROM hhc_web.online_bulletin_learning_example e WHERE signature=$1 AND `+effectiveLearningExample, signature).Scan(&issues, &kinds, &effectiveKind); err != nil {
			return err
		}
		next := status
		if status != "disabled" {
			switch {
			case kinds > 1:
				next = "conflicted"
			case issues >= 2:
				next = "active"
			default:
				next = "pending"
			}
		}
		nextKind := kind
		if kinds == 1 {
			nextKind = effectiveKind
		}
		if next != status || nextKind != kind {
			if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_learning_rule SET version=version+1,status=$2,kind=$3 WHERE signature=$1`, signature, next, nextKind); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_learning_rule_revision(signature,version,status,kind,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6)`, signature, version+1, next, nextKind, actor, now); err != nil {
				return err
			}
		}
	}

	return nil
}

func loadActiveLearningRules(ctx context.Context, tx *sql.Tx) ([]byte, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.signature,r.version,r.kind FROM hhc_web.online_bulletin_learning_rule r WHERE r.status='active' AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_learning_example e WHERE e.signature=r.signature AND `+effectiveLearningExample+` GROUP BY e.signature HAVING count(DISTINCT e.issue_id)>=2 AND count(DISTINCT e.kind)=1 AND min(e.kind)=r.kind) ORDER BY r.signature LIMIT 4097`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []onlinebulletins.LearningRuleInput{}
	for rows.Next() {
		var rule onlinebulletins.LearningRuleInput
		if err = rows.Scan(&rule.Signature, &rule.Version, &rule.Kind); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return learningRulesSnapshot(ctx, rules)
}

func learningRulesSnapshot(ctx context.Context, rules []onlinebulletins.LearningRuleInput) ([]byte, error) {
	// Capacity is the only safe fallback. Invalid persisted values still fail.
	for _, rule := range rules {
		if !onlinebulletins.ValidLearningRules([]onlinebulletins.LearningRuleInput{rule}) {
			return nil, onlinebulletins.ErrExtractionJob
		}
	}
	if len(rules) > onlinebulletins.MaxLearningRules {
		slog.WarnContext(ctx, "bulletin classification rules skipped", "reason", "learning_rules_capacity", "limit", onlinebulletins.MaxLearningRules)
		return []byte("[]"), nil
	}
	if !onlinebulletins.ValidLearningRules(rules) {
		return nil, onlinebulletins.ErrExtractionJob
	}
	return json.Marshal(rules)
}
