package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// The canonical revision write is shared by issue edits, edition edits, PDF
// replacement and CMS restore. All callers already hold the issue lock and
// audit the encompassing CMS mutation in this transaction.
func invalidateOnlineMetadata(ctx context.Context, tx *sql.Tx, issue bulletins.Issue, actor string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT d.id::text,d.version,r.document_json,r.review_issues,r.extractor_version,r.content_hash
	 FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision
	 WHERE d.issue_id=$1 ORDER BY d.id FOR UPDATE OF d`, issue.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type local struct {
		result          onlinebulletins.SavedDraft
		document        onlinebulletins.Document
		issues          []onlinebulletins.ReviewIssue
		extractor, hash string
	}
	var locals []local
	for rows.Next() {
		var value local
		var documentJSON, issuesJSON []byte
		if err = rows.Scan(&value.result.DocumentID, &value.result.Version, &documentJSON, &issuesJSON, &value.extractor, &value.hash); err != nil {
			return err
		}
		if json.Unmarshal(documentJSON, &value.document) != nil || json.Unmarshal(issuesJSON, &value.issues) != nil {
			return bulletins.ErrConflict
		}
		locals = append(locals, value)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(locals) > 0 && issue.IssueNumber == nil {
		return bulletins.ErrConflict
	}
	for _, value := range locals {
		var canonical onlinebulletins.CanonicalMetadata
		found := false
		for _, version := range issue.Versions {
			if version.Series == value.document.Series && version.Locale == value.document.ContentLocale {
				canonical = onlinebulletins.CanonicalMetadata{Title: version.Title, Subtitle: version.Subtitle, IssueNumber: *issue.IssueNumber, Date: issue.IssueDate}
				found = true
				break
			}
		}
		if !found {
			return bulletins.ErrOnlineDocumentExists
		}
		value.document.LayoutManifest.ContentHash, value.document.LayoutManifest.LayoutValidationHash = "", ""
		_, contentHash, err := onlinebulletins.MarshalLayoutSubmission(value.document, canonical)
		if err != nil {
			return err
		}
		// Every immutable revision hashes content plus its canonical rendering
		// snapshot. Unrelated edition edits and no-op saves must not fork Local.
		if contentHash == value.hash {
			continue
		}
		if _, err = writeOnlineLocal(ctx, tx, value.result, value.document, value.issues, value.extractor, canonical, actor, now); err != nil {
			return err
		}
		metadata, err := json.Marshal(canonical)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document d SET metadata_sync_pending=EXISTS(SELECT 1 FROM hhc_web.online_bulletin_publication p WHERE p.document_id=d.id AND p.revision=d.published_revision AND p.canonical_metadata<>$2::jsonb) WHERE d.id=$1`, value.result.DocumentID, metadata); err != nil {
			return err
		}
	}
	return nil
}
