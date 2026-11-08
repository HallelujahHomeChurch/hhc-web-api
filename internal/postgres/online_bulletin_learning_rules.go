package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// A confirmation is also a withdrawal of all earlier votes from that document.
// Use the latest confirmed revision even when its event has not run yet.
const effectiveLearningExample = `NOT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_confirmation newer WHERE newer.document_id=e.document_id AND newer.revision>e.revision)`

const learningRuleFields = `r.signature,r.version,r.status,r.kind,r.title,r.speaker,count(DISTINCT e.issue_id),count(DISTINCT e.kind)>1`

func scanLearningRule(row interface{ Scan(...any) error }) (onlinebulletins.LearningRule, error) {
	var value onlinebulletins.LearningRule
	err := row.Scan(&value.Signature, &value.Version, &value.Status, &value.Kind, &value.Title, &value.Speaker, &value.ConfirmedIssues, &value.Conflicted)
	if errors.Is(err, sql.ErrNoRows) {
		err = bulletins.ErrNotFound
	}
	return value, err
}

func (r *Repository) OnlineBulletinLearningRules(ctx context.Context, cursor string) (onlinebulletins.LearningRulePage, error) {
	result := onlinebulletins.LearningRulePage{Items: []onlinebulletins.LearningRule{}}
	if cursor != "" && !onlinebulletins.ValidLearningSignature(cursor) {
		return result, bulletins.ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, `WITH selected AS (SELECT * FROM hhc_web.online_bulletin_learning_rule WHERE signature>$1 ORDER BY signature LIMIT 51)
	 SELECT `+learningRuleFields+` FROM selected r LEFT JOIN hhc_web.online_bulletin_learning_example e ON e.signature=r.signature AND `+effectiveLearningExample+`
	 GROUP BY r.signature,r.version,r.status,r.kind,r.title,r.speaker ORDER BY r.signature`, cursor)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		value, err := scanLearningRule(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, value)
	}
	if len(result.Items) > 50 {
		result.Items = result.Items[:50]
		result.NextCursor = result.Items[49].Signature
	}
	return result, rows.Err()
}

func (r *Repository) OnlineBulletinLearningRule(ctx context.Context, signature string) (onlinebulletins.LearningRuleDetail, error) {
	result := onlinebulletins.LearningRuleDetail{Revisions: []onlinebulletins.LearningRuleRevision{}, Examples: []onlinebulletins.LearningExample{}}
	if !onlinebulletins.ValidLearningSignature(signature) {
		return result, bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result.Rule, err = scanLearningRule(tx.QueryRowContext(ctx, `SELECT `+learningRuleFields+` FROM hhc_web.online_bulletin_learning_rule r LEFT JOIN hhc_web.online_bulletin_learning_example e ON e.signature=r.signature AND `+effectiveLearningExample+` WHERE r.signature=$1 GROUP BY r.signature`, signature))
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT version,status,kind,created_by,created_at FROM hhc_web.online_bulletin_learning_rule_revision WHERE signature=$1 ORDER BY version DESC LIMIT 50`, signature)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var value onlinebulletins.LearningRuleRevision
		var created time.Time
		if err = rows.Scan(&value.Version, &value.Status, &value.Kind, &value.CreatedBy, &created); err != nil {
			rows.Close()
			return result, err
		}
		value.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		result.Revisions = append(result.Revisions, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT e.issue_id::text,i.issue_number,e.document_id::text,e.revision,e.component_id,e.source_page,e.kind,e.created_by,e.created_at,(SELECT max(c.revision) FROM hhc_web.online_bulletin_confirmation c WHERE c.document_id=e.document_id AND c.revision>e.revision) FROM hhc_web.online_bulletin_learning_example e JOIN hhc_web.bulletin_issue i ON i.id=e.issue_id WHERE signature=$1 ORDER BY e.created_at DESC,e.document_id,e.revision DESC,e.component_id LIMIT 50`, signature)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var value onlinebulletins.LearningExample
		var created time.Time
		if err = rows.Scan(&value.IssueID, &value.IssueNumber, &value.DocumentID, &value.Revision, &value.ComponentID, &value.SourcePage, &value.Kind, &value.CreatedBy, &created, &value.SupersededByRevision); err != nil {
			rows.Close()
			return result, err
		}
		value.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		result.Examples = append(result.Examples, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (r *Repository) SetOnlineBulletinLearningRuleEnabled(ctx context.Context, signature string, enabled bool, expected int64, actor string, now time.Time) (onlinebulletins.LearningRule, error) {
	var result onlinebulletins.LearningRule
	if !onlinebulletins.ValidLearningSignature(signature) || expected < 1 || actor == "" || now.IsZero() {
		return result, bulletins.ErrInvalid
	}
	if r.auditOutbox == nil {
		return result, bulletins.ErrAuditUnavailable
	}
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return result, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1739))`, signature); err != nil {
		return result, err
	}
	// Confirmation holds the same signature lock before adding any evidence.
	if err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.online_bulletin_learning_rule WHERE signature=$1 FOR UPDATE`, signature).Scan(&result.Version); errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	} else if err != nil {
		return result, err
	}
	if result.Version != expected {
		return result, bulletins.ErrPrecondition
	}
	result, err = scanLearningRule(tx.QueryRowContext(ctx, `SELECT `+learningRuleFields+` FROM hhc_web.online_bulletin_learning_rule r LEFT JOIN hhc_web.online_bulletin_learning_example e ON e.signature=r.signature AND `+effectiveLearningExample+` WHERE r.signature=$1 GROUP BY r.signature`, signature))
	if err != nil {
		return result, err
	}
	status := "disabled"
	if enabled {
		if result.Conflicted || result.ConfirmedIssues < 2 {
			return result, bulletins.ErrConflict
		}
		status = "active"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_learning_rule SET version=version+1,status=$2 WHERE signature=$1`, signature, status); err != nil {
		return result, err
	}
	result.Version++
	result.Status = status
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_learning_rule_revision(signature,version,status,kind,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6)`, signature, result.Version, status, result.Kind, actor, now); err != nil {
		return result, err
	}
	// Rule history identifies the change; the existing church-settings audit owns it.
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.site_settings.update", "singleton", actor, now); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
