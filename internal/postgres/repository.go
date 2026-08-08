package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) CreateIssue(ctx context.Context, number int, date, actor, idempotency string, now time.Time) (bulletins.Issue, error) {
	id := platform.NewID()
	_, err := r.db.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,'draft',1,$4,$5,$5,$6,$6) ON CONFLICT(idempotency_key) DO NOTHING`, id, number, date, idempotency, actor, now)
	if err != nil {
		return bulletins.Issue{}, mapConflict(err)
	}
	var existingID, existingDate string
	var existingNumber sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT id::text,issue_number,issue_date::text FROM hhc_web.bulletin_issue WHERE idempotency_key=$1`, idempotency).Scan(&existingID, &existingNumber, &existingDate); err != nil {
		return bulletins.Issue{}, err
	}
	if !existingNumber.Valid || int(existingNumber.Int64) != number || existingDate != date {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	return r.GetIssue(ctx, existingID)
}

func (r *Repository) ListIssues(ctx context.Context, page, size int, status, query string) (bulletins.Page, error) {
	args := []any{}
	clauses := []string{}
	if status != "" {
		args = append(args, status)
		clauses = append(clauses, fmt.Sprintf("i.status=$%d", len(args)))
	}
	if query != "" {
		args = append(args, "%"+query+"%")
		clauses = append(clauses, fmt.Sprintf("(i.issue_number::text ILIKE $%d OR EXISTS (SELECT 1 FROM hhc_web.bulletin_version v WHERE v.issue_id=i.id AND (v.title ILIKE $%d OR v.subtitle ILIKE $%d)))", len(args), len(args), len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM hhc_web.bulletin_issue i"+where, args...).Scan(&total); err != nil {
		return bulletins.Page{}, err
	}
	args = append(args, size, (page-1)*size)
	limitPos, offsetPos := len(args)-1, len(args)
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT i.id::text,i.issue_number,i.issue_date::text,i.status,i.version,i.created_by,i.updated_by,i.published_at,i.created_at,i.updated_at FROM hhc_web.bulletin_issue i%s ORDER BY i.issue_number DESC NULLS LAST,i.issue_date DESC LIMIT $%d OFFSET $%d`, where, limitPos, offsetPos), args...)
	if err != nil {
		return bulletins.Page{}, err
	}
	defer rows.Close()
	items := []bulletins.Issue{}
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return bulletins.Page{}, err
		}
		items = append(items, issue)
	}
	if err := rows.Err(); err != nil {
		return bulletins.Page{}, err
	}
	if err := rows.Close(); err != nil {
		return bulletins.Page{}, err
	}
	ids := make([]string, len(items))
	for index := range items {
		ids[index] = items[index].ID
	}
	versions, err := r.versions(ctx, ids...)
	if err != nil {
		return bulletins.Page{}, err
	}
	byIssue := make(map[string][]bulletins.Version, len(items))
	for _, version := range versions {
		byIssue[version.IssueID] = append(byIssue[version.IssueID], version)
	}
	for index := range items {
		items[index].Versions = byIssue[items[index].ID]
		if items[index].Versions == nil {
			items[index].Versions = []bulletins.Version{}
		}
	}
	return bulletins.Page{Items: items, Page: page, PageSize: size, Total: total}, nil
}

func (r *Repository) GetIssue(ctx context.Context, id string) (bulletins.Issue, error) {
	return loadIssue(ctx, r.db, id)
}

func (r *Repository) UpdateIssue(ctx context.Context, id string, expected int64, input bulletins.UpdateIssueInput, actor string, now time.Time) (bulletins.Issue, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	if err := lockMutableIssue(ctx, tx, id, expected); err != nil {
		return bulletins.Issue{}, err
	}
	var published bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND (status IN ('published','publishing','unpublishing','unpublish_failed') OR COALESCE(public_grant_id,'')<>'' OR COALESCE(retiring_grant_id,'')<>''))`, id).Scan(&published); err != nil {
		return bulletins.Issue{}, err
	}
	if published {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET issue_number=$2,issue_date=$3,version=version+1,updated_by=$4,updated_at=$5 WHERE id=$1`, id, input.IssueNumber, input.IssueDate, actor, now); err != nil {
		return bulletins.Issue{}, mapConflict(err)
	}
	issue, err := loadIssue(ctx, tx, id)
	if err != nil {
		return bulletins.Issue{}, err
	}
	if err := insertBulletinRevision(ctx, tx, issue, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return issue, nil
}

type bulletinQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadIssue(ctx context.Context, query bulletinQueryer, id string) (bulletins.Issue, error) {
	issue, err := scanIssue(query.QueryRowContext(ctx, `SELECT id::text,issue_number,issue_date::text,status,version,created_by,updated_by,published_at,created_at,updated_at FROM hhc_web.bulletin_issue WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	}
	if err != nil {
		return bulletins.Issue{}, err
	}
	issue.Versions, err = queryVersions(ctx, query, id)
	return issue, err
}

type scanner interface{ Scan(...any) error }

func scanIssue(row scanner) (bulletins.Issue, error) {
	var v bulletins.Issue
	var number sql.NullInt64
	var published sql.NullTime
	err := row.Scan(&v.ID, &number, &v.IssueDate, &v.Status, &v.Version, &v.CreatedBy, &v.UpdatedBy, &published, &v.CreatedAt, &v.UpdatedAt)
	if number.Valid {
		value := int(number.Int64)
		v.IssueNumber = &value
	}
	if published.Valid {
		value := published.Time
		v.PublishedAt = &value
	}
	return v, err
}
func (r *Repository) versions(ctx context.Context, ids ...string) ([]bulletins.Version, error) {
	return queryVersions(ctx, r.db, ids...)
}
func queryVersions(ctx context.Context, query bulletinQueryer, ids ...string) ([]bulletins.Version, error) {
	if len(ids) == 0 {
		return []bulletins.Version{}, nil
	}
	args := make([]any, len(ids))
	placeholders := make([]string, len(ids))
	for index, id := range ids {
		args[index] = id
		placeholders[index] = fmt.Sprintf("$%d", index+1)
	}
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`
		SELECT v.id::text,v.issue_id::text,v.locale,v.title,v.subtitle,v.pdf_asset_id,v.pdf_file_name,
		       COALESCE(v.public_grant_id,''),v.status,COALESCE(w.status,''),COALESCE(w.error_detail,''),
		       v.version,v.published_at,v.created_at,v.updated_at
		FROM hhc_web.bulletin_version v
		LEFT JOIN LATERAL (
			SELECT status,error_detail
			FROM hhc_web.publication_workflow
			WHERE resource_id=v.issue_id AND locale=v.locale
			ORDER BY created_at DESC
			LIMIT 1
		) w ON true
		WHERE v.issue_id IN (%s)
		ORDER BY v.issue_id,v.locale`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []bulletins.Version{}
	for rows.Next() {
		var v bulletins.Version
		var published sql.NullTime
		if err := rows.Scan(&v.ID, &v.IssueID, &v.Locale, &v.Title, &v.Subtitle, &v.PDFAssetID, &v.PDFFileName, &v.PublicGrantID, &v.Status, &v.WorkflowStatus, &v.WorkflowError, &v.Version, &published, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		if published.Valid {
			value := published.Time
			v.PublishedAt = &value
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (r *Repository) PutVersion(ctx context.Context, id string, expected int64, input bulletins.PutVersionInput, actor string, now time.Time) (bulletins.Issue, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	var current int64
	var issueStatus string
	if err := tx.QueryRowContext(ctx, `SELECT version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current, &issueStatus); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	if current != expected {
		return bulletins.Issue{}, bulletins.ErrPrecondition
	}
	if issueStatus == "publishing" || issueStatus == "unpublishing" {
		return bulletins.Issue{}, bulletins.ErrNotPublishable
	}
	var existingStatus string
	err = tx.QueryRowContext(ctx, `SELECT status FROM hhc_web.bulletin_version WHERE issue_id=$1 AND locale=$2`, id, input.Locale).Scan(&existingStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, err
	}
	if existingStatus == "unpublishing" || existingStatus == "unpublish_failed" {
		return bulletins.Issue{}, bulletins.ErrNotPublishable
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO hhc_web.bulletin_version(id,issue_id,locale,title,subtitle,pdf_asset_id,pdf_file_name,status,version,created_by,updated_by,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,'draft',1,$8,$8,$9,$9)
		ON CONFLICT(issue_id,locale) DO UPDATE SET
			retiring_asset_id=CASE WHEN hhc_web.bulletin_version.status='published' THEN hhc_web.bulletin_version.pdf_asset_id ELSE hhc_web.bulletin_version.retiring_asset_id END,
			retiring_grant_id=CASE WHEN hhc_web.bulletin_version.status='published' THEN hhc_web.bulletin_version.public_grant_id ELSE hhc_web.bulletin_version.retiring_grant_id END,
			title=EXCLUDED.title,subtitle=EXCLUDED.subtitle,pdf_asset_id=EXCLUDED.pdf_asset_id,pdf_file_name=EXCLUDED.pdf_file_name,status='draft',
			version=hhc_web.bulletin_version.version+1,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
		platform.NewID(), id, input.Locale, input.Title, input.Subtitle, input.PDFAssetID, input.PDFFileName, actor, now)
	if err != nil {
		return bulletins.Issue{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='draft',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, id, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	issue, err := loadIssue(ctx, tx, id)
	if err != nil {
		return bulletins.Issue{}, err
	}
	if err := insertBulletinRevision(ctx, tx, issue, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return issue, nil
}

func (r *Repository) UpdateVersion(ctx context.Context, id, locale string, expected int64, title, subtitle, actor string, now time.Time) (bulletins.Issue, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	if err := lockMutableIssue(ctx, tx, id, expected); err != nil {
		return bulletins.Issue{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET title=$3,subtitle=$4,status='draft',version=version+1,updated_by=$5,updated_at=$6 WHERE issue_id=$1 AND locale=$2 AND status IN ('draft','unpublished') AND COALESCE(public_grant_id,'')='' AND COALESCE(retiring_grant_id,'')=''`, id, locale, title, subtitle, actor, now)
	if err != nil {
		return bulletins.Issue{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	return finishIssueDraft(ctx, tx, id, actor, now)
}

func (r *Repository) DeleteVersion(ctx context.Context, id, locale string, expected int64, actor string, now time.Time) (bulletins.Issue, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	if err := lockMutableIssue(ctx, tx, id, expected); err != nil {
		return bulletins.Issue{}, err
	}
	var assetID, status, publicGrantID, retiringGrantID string
	if err := tx.QueryRowContext(ctx, `SELECT pdf_asset_id,status,COALESCE(public_grant_id,''),COALESCE(retiring_grant_id,'') FROM hhc_web.bulletin_version WHERE issue_id=$1 AND locale=$2 FOR UPDATE`, id, locale).Scan(&assetID, &status, &publicGrantID, &retiringGrantID); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	if status != "draft" && status != "unpublished" || publicGrantID != "" || retiringGrantID != "" {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_version WHERE issue_id=$1 AND locale=$2`, id, locale); err != nil {
		return bulletins.Issue{}, err
	}
	if err := enqueueAssetDeletes(ctx, tx, "bulletin", id, expected, []string{assetID}, now); err != nil {
		return bulletins.Issue{}, err
	}
	return finishIssueDraft(ctx, tx, id, actor, now)
}

func lockMutableIssue(ctx context.Context, tx *sql.Tx, id string, expected int64) error {
	var current int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current, &status); errors.Is(err, sql.ErrNoRows) {
		return bulletins.ErrNotFound
	} else if err != nil {
		return err
	}
	if current != expected {
		return bulletins.ErrPrecondition
	}
	if status == "publishing" || status == "unpublishing" {
		return bulletins.ErrConflict
	}
	return nil
}

func finishIssueDraft(ctx context.Context, tx *sql.Tx, id, actor string, now time.Time) (bulletins.Issue, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='draft',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, id, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	issue, err := loadIssue(ctx, tx, id)
	if err != nil {
		return bulletins.Issue{}, err
	}
	if err := insertBulletinRevision(ctx, tx, issue, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return issue, nil
}

func (r *Repository) IssueRevisions(ctx context.Context, id string) ([]bulletins.Revision, error) {
	var exists bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_issue WHERE id=$1)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, bulletins.ErrNotFound
	}
	rows, err := r.db.QueryContext(ctx, `SELECT version,snapshot_json,created_by,created_at FROM hhc_web.bulletin_revision WHERE issue_id=$1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []bulletins.Revision{}
	for rows.Next() {
		var value bulletins.Revision
		var payload []byte
		if err := rows.Scan(&value.Version, &payload, &value.CreatedBy, &value.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &value.Snapshot); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Repository) RestoreIssueRevision(ctx context.Context, id string, revision, expected int64, actor string, now time.Time) (bulletins.Issue, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	var current int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current, &status); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	if current != expected {
		return bulletins.Issue{}, bulletins.ErrPrecondition
	}
	if status == "publishing" || status == "unpublishing" {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	var payload []byte
	if err := tx.QueryRowContext(ctx, `SELECT snapshot_json FROM hhc_web.bulletin_revision WHERE issue_id=$1 AND version=$2`, id, revision).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	var snapshot bulletins.Issue
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return bulletins.Issue{}, err
	}
	if snapshot.ID != id || !validStoredIssueDate(snapshot.IssueDate) {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	locales := make([]string, len(snapshot.Versions))
	args := []any{id}
	for index, version := range snapshot.Versions {
		locales[index] = version.Locale
		args = append(args, version.Locale)
	}
	missingClause := ""
	if len(locales) > 0 {
		placeholders := make([]string, len(locales))
		for index := range locales {
			placeholders[index] = fmt.Sprintf("$%d", index+2)
		}
		missingClause = " AND locale NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	var hasPublicVersion bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1`+missingClause+` AND (COALESCE(public_grant_id,'')<>'' OR COALESCE(retiring_grant_id,'')<>'' OR status IN ('published','unpublishing','unpublish_failed')))`, args...).Scan(&hasPublicVersion); err != nil {
		return bulletins.Issue{}, err
	}
	if hasPublicVersion {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_version WHERE issue_id=$1`+missingClause, args...); err != nil {
		return bulletins.Issue{}, err
	}
	for _, version := range snapshot.Versions {
		versionID := version.ID
		if versionID == "" {
			versionID = platform.NewID()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.bulletin_version(id,issue_id,locale,title,subtitle,pdf_asset_id,pdf_file_name,status,version,created_by,updated_by,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,'draft',1,$8,$8,$9,$9)
			ON CONFLICT(issue_id,locale) DO UPDATE SET
				retiring_asset_id=CASE WHEN hhc_web.bulletin_version.status='published' THEN hhc_web.bulletin_version.pdf_asset_id ELSE hhc_web.bulletin_version.retiring_asset_id END,
				retiring_grant_id=CASE WHEN hhc_web.bulletin_version.status='published' THEN hhc_web.bulletin_version.public_grant_id ELSE hhc_web.bulletin_version.retiring_grant_id END,
				title=EXCLUDED.title,subtitle=EXCLUDED.subtitle,pdf_asset_id=EXCLUDED.pdf_asset_id,pdf_file_name=EXCLUDED.pdf_file_name,status='draft',
				version=hhc_web.bulletin_version.version+1,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
			versionID, id, version.Locale, version.Title, version.Subtitle, version.PDFAssetID, version.PDFFileName, actor, now); err != nil {
			return bulletins.Issue{}, err
		}
	}
	next := current + 1
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET issue_number=$2,issue_date=$3,status='draft',version=$4,updated_by=$5,updated_at=$6 WHERE id=$1`, id, snapshot.IssueNumber, snapshot.IssueDate, next, actor, now); err != nil {
		return bulletins.Issue{}, mapConflict(err)
	}
	restored, err := loadIssue(ctx, tx, id)
	if err != nil {
		return bulletins.Issue{}, err
	}
	if err := insertBulletinRevision(ctx, tx, restored, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return restored, nil
}

func insertBulletinRevision(ctx context.Context, tx *sql.Tx, issue bulletins.Issue, actor string, now time.Time) error {
	payload, err := json.Marshal(issue)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_revision(issue_id,version,snapshot_json,created_by,created_at) VALUES($1,$2,$3,$4,$5)`, issue.ID, issue.Version, payload, actor, now)
	return err
}

func validStoredIssueDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func (r *Repository) StartPublish(ctx context.Context, id, locale string, expected int64, actor string, now time.Time) (bulletins.Workflow, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Workflow{}, err
	}
	defer tx.Rollback()
	var current int64
	var issueStatus string
	if err := tx.QueryRowContext(ctx, `SELECT version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current, &issueStatus); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Workflow{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Workflow{}, err
	}
	if current != expected {
		return bulletins.Workflow{}, bulletins.ErrPrecondition
	}
	if issueStatus == "publishing" || issueStatus == "unpublishing" {
		return bulletins.Workflow{}, bulletins.ErrNotPublishable
	}
	var assetID string
	if err := tx.QueryRowContext(ctx, `SELECT pdf_asset_id FROM hhc_web.bulletin_version WHERE issue_id=$1 AND locale=$2 FOR UPDATE`, id, locale).Scan(&assetID); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Workflow{}, bulletins.ErrNotPublishable
	} else if err != nil {
		return bulletins.Workflow{}, err
	}
	next := current + 1
	workflow := bulletins.Workflow{ID: platform.NewID(), Status: "waiting_asset_scan", AggregateVersion: next, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='publishing',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, next, actor, now); err != nil {
		return bulletins.Workflow{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='publishing',version=version+1,updated_by=$3,updated_at=$4 WHERE issue_id=$1 AND locale=$2`, id, locale, actor, now); err != nil {
		return bulletins.Workflow{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.publication_workflow(id,workflow_type,resource_type,resource_id,locale,aggregate_version,asset_id,status,created_by,created_at,updated_at) VALUES($1,'bulletin_publish','bulletin',$2,$3,$4,$5,$6,$7,$8,$8)`, workflow.ID, id, locale, next, assetID, workflow.Status, actor, now); err != nil {
		return bulletins.Workflow{}, err
	}
	payload, _ := json.Marshal(map[string]any{"workflowId": workflow.ID, "issueId": id, "locale": locale, "assetId": assetID, "aggregateVersion": next})
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'asset-api','bulletin.publish.ensure_asset','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)`, platform.NewID(), id, next, payload, fmt.Sprintf("bulletin:%s:%s:publish:v%d", id, locale, next), now); err != nil {
		return bulletins.Workflow{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Workflow{}, err
	}
	return workflow, nil
}

func (r *Repository) Unpublish(ctx context.Context, id, locale string, expected int64, actor string, now time.Time) (bulletins.Issue, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	var current int64
	var issueStatus string
	if err := tx.QueryRowContext(ctx, `SELECT version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current, &issueStatus); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	if current != expected {
		return bulletins.Issue{}, bulletins.ErrPrecondition
	}
	if issueStatus == "publishing" || issueStatus == "unpublishing" {
		return bulletins.Issue{}, bulletins.ErrNotPublishable
	}
	var assetID, grantID, date string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(NULLIF(v.retiring_asset_id,''),v.pdf_asset_id),COALESCE(v.public_grant_id,''),i.issue_date::text
		FROM hhc_web.bulletin_version v
		JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id
		WHERE v.issue_id=$1 AND v.locale=$2 AND v.status IN ('published','draft','unpublish_failed') AND COALESCE(v.public_grant_id,'')<>''
		FOR UPDATE`, id, locale).Scan(&assetID, &grantID, &date); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotPublishable
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	next := current + 1
	workflowID := platform.NewID()
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='unpublishing',version=version+1,updated_by=$3,updated_at=$4 WHERE issue_id=$1 AND locale=$2`, id, locale, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='unpublishing',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, next, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.publication_workflow(id,workflow_type,resource_type,resource_id,locale,aggregate_version,asset_id,status,created_by,created_at,updated_at) VALUES($1,'bulletin_unpublish','bulletin',$2,$3,$4,$5,'revoke_pending',$6,$7,$7)`, workflowID, id, locale, next, assetID, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE projection_key=$1 OR (projection_key=$2 AND resource_id=$3)`, fmt.Sprintf("bulletins:issue:%s:%s", locale, date), fmt.Sprintf("bulletins:latest:%s", locale), id); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) SELECT $1,resource_type,resource_id,locale,'/bulletins/latest',version,etag,payload_json,$3 FROM hhc_web.public_projection WHERE resource_type='bulletin_issue' AND locale=$2 ORDER BY COALESCE((payload_json->>'issueNumber')::integer,0) DESC,payload_json->>'issueDate' DESC LIMIT 1 ON CONFLICT(projection_key) DO UPDATE SET resource_id=EXCLUDED.resource_id,version=EXCLUDED.version,etag=EXCLUDED.etag,payload_json=EXCLUDED.payload_json,updated_at=EXCLUDED.updated_at`, fmt.Sprintf("bulletins:latest:%s", locale), locale, now); err != nil {
		return bulletins.Issue{}, err
	}
	payload, _ := json.Marshal(publication.UnpublishPayload{WorkflowID: workflowID, IssueID: id, Locale: locale, AssetID: assetID, GrantID: grantID, AggregateVersion: next})
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'asset-api','bulletin.unpublish.revoke_asset','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)`, platform.NewID(), id, next, payload, fmt.Sprintf("bulletin:%s:%s:unpublish:v%d", id, locale, next), now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return r.GetIssue(ctx, id)
}

func (r *Repository) DeleteIssue(ctx context.Context, id string, expected int64, actor string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var current int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current, &status); errors.Is(err, sql.ErrNoRows) {
		return bulletins.ErrNotFound
	} else if err != nil {
		return err
	}
	if current != expected {
		return bulletins.ErrPrecondition
	}
	if status == "publishing" || status == "published" || status == "unpublishing" || status == "unpublish_failed" {
		return bulletins.ErrConflict
	}

	var hasPublicState bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM hhc_web.public_projection
			WHERE resource_type IN ('bulletin_issue','bulletin_latest') AND resource_id=$1
		) OR EXISTS(
			SELECT 1 FROM hhc_web.bulletin_version
			WHERE issue_id=$1 AND (
				COALESCE(public_grant_id,'')<>'' OR COALESCE(retiring_grant_id,'')<>''
			)
		)`, id).Scan(&hasPublicState); err != nil {
		return err
	}
	if hasPublicState {
		return bulletins.ErrConflict
	}
	assetIDs, err := bulletinAssetIDs(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := insertDeleteAudit(ctx, tx, "bulletin", id, current, actor, assetIDs, now); err != nil {
		return err
	}
	if err := enqueueAssetDeletes(ctx, tx, "bulletin", id, current, assetIDs, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type IN ('bulletin_issue','bulletin_latest') AND resource_id=$1`, id); err != nil {
		return err
	}
	if result, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, id); err != nil {
		return err
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		return bulletins.ErrNotFound
	}
	return tx.Commit()
}

func bulletinAssetIDs(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT asset_id FROM (
			SELECT pdf_asset_id AS asset_id FROM hhc_web.bulletin_version WHERE issue_id=$1
			UNION
			SELECT snapshot_version->>'pdfAssetId'
			FROM hhc_web.bulletin_revision revision
			CROSS JOIN LATERAL jsonb_array_elements(revision.snapshot_json->'versions') snapshot_version
			WHERE revision.issue_id=$1
		) assets WHERE COALESCE(asset_id,'')<>'' ORDER BY asset_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Repository) GetPublicLatest(ctx context.Context, locale string) (bulletins.PublicBulletin, error) {
	return r.publicProjection(ctx, fmt.Sprintf("bulletins:latest:%s", locale))
}
func (r *Repository) GetPublicByDate(ctx context.Context, date, locale string) (bulletins.PublicBulletin, error) {
	return r.publicProjection(ctx, fmt.Sprintf("bulletins:issue:%s:%s", locale, date))
}
func (r *Repository) GetPublicByNumber(ctx context.Context, issueNumber int, locale string) (bulletins.PublicBulletin, error) {
	var payload []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT p.payload_json
		FROM hhc_web.bulletin_issue i
		JOIN hhc_web.public_projection p
		  ON p.resource_type='bulletin_issue'
		 AND p.resource_id=i.id
		 AND p.locale=$2
		WHERE i.issue_number=$1`, issueNumber, locale).Scan(&payload)
	return decodePublicBulletin(payload, err)
}
func (r *Repository) publicProjection(ctx context.Context, key string) (bulletins.PublicBulletin, error) {
	var payload []byte
	err := r.db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE projection_key=$1`, key).Scan(&payload)
	return decodePublicBulletin(payload, err)
}
func decodePublicBulletin(payload []byte, err error) (bulletins.PublicBulletin, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return bulletins.PublicBulletin{}, bulletins.ErrNotFound
	}
	if err != nil {
		return bulletins.PublicBulletin{}, err
	}
	var v bulletins.PublicBulletin
	if err := json.Unmarshal(payload, &v); err != nil {
		return bulletins.PublicBulletin{}, err
	}
	return v, nil
}
func (r *Repository) ListPublic(ctx context.Context, page, size int) (bulletins.PublicPage, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(DISTINCT resource_id) FROM hhc_web.public_projection WHERE resource_type='bulletin_issue'`).Scan(&total); err != nil {
		return bulletins.PublicPage{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH issues AS (
			SELECT resource_id, max(COALESCE((payload_json->>'issueNumber')::integer,0)) AS issue_number, max(payload_json->>'issueDate') AS issue_date
			FROM hhc_web.public_projection
			WHERE resource_type='bulletin_issue'
			GROUP BY resource_id
			ORDER BY issue_number DESC,issue_date DESC
			LIMIT $1 OFFSET $2
		)
		SELECT p.payload_json
		FROM issues i
		JOIN hhc_web.public_projection p
		  ON p.resource_type='bulletin_issue' AND p.resource_id=i.resource_id
		ORDER BY i.issue_number DESC,i.issue_date DESC,
		  CASE p.locale WHEN 'zh-Hant' THEN 1 WHEN 'zh-Hans' THEN 2 ELSE 3 END`,
		size, (page-1)*size)
	if err != nil {
		return bulletins.PublicPage{}, err
	}
	defer rows.Close()
	items := []bulletins.PublicIssue{}
	for rows.Next() {
		var payload []byte
		var item bulletins.PublicBulletin
		if err := rows.Scan(&payload); err != nil {
			return bulletins.PublicPage{}, err
		}
		if err := json.Unmarshal(payload, &item); err != nil {
			return bulletins.PublicPage{}, err
		}
		if len(items) == 0 || items[len(items)-1].IssueDate != item.IssueDate {
			items = append(items, bulletins.PublicIssue{IssueNumber: item.IssueNumber, IssueDate: item.IssueDate, Versions: []bulletins.PublicBulletin{}})
		}
		items[len(items)-1].Versions = append(items[len(items)-1].Versions, item)
	}
	return bulletins.PublicPage{Items: items, Page: page, PageSize: size, Total: total}, rows.Err()
}

func (r *Repository) Claim(ctx context.Context, now time.Time, lease time.Duration) (publication.Event, bool, error) {
	var event publication.Event
	err := r.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id FROM hhc_web.outbox_event
			WHERE (status='pending' AND next_attempt_at <= $1)
			   OR (status='processing' AND claimed_until <= $1)
			ORDER BY next_attempt_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE hhc_web.outbox_event e
		SET status='processing',attempts=e.attempts+1,claimed_until=$2,updated_at=$1
		FROM candidate
		WHERE e.id=candidate.id
		RETURNING e.id::text,e.event_type,e.aggregate_id::text,e.aggregate_version,e.payload_json,e.attempts,e.created_at`, now, now.Add(lease)).Scan(
		&event.ID, &event.EventType, &event.AggregateID, &event.AggregateVersion, &event.Payload, &event.Attempts, &event.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return publication.Event{}, false, nil
	}
	return event, err == nil, err
}

func (r *Repository) Retry(ctx context.Context, id, detail string, nextAttempt, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='pending',next_attempt_at=$2,claimed_until=NULL,last_error=$3,updated_at=$4 WHERE id=$1 AND status='processing'`, id, nextAttempt, detail, now)
	return err
}

func (r *Repository) Defer(ctx context.Context, id, detail string, nextAttempt, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='pending',attempts=GREATEST(attempts-1,0),next_attempt_at=$2,claimed_until=NULL,last_error=$3,updated_at=$4 WHERE id=$1 AND status='processing'`, id, nextAttempt, detail, now)
	return err
}

func (r *Repository) Fail(ctx context.Context, event publication.Event, detail string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := failEvent(ctx, tx, event, detail, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) FailPublish(ctx context.Context, event publication.Event, assetID, grantID, detail string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := failEvent(ctx, tx, event, detail, now); err != nil {
		return err
	}
	payload, err := json.Marshal(publication.ContentUnpublishPayload{AssetID: assetID, GrantID: grantID, AggregateVersion: event.AggregateVersion})
	if err != nil {
		return err
	}
	aggregateType := "bulletin"
	if strings.HasPrefix(event.EventType, "news.") {
		aggregateType = "news"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
		VALUES($1,'asset-api','asset.grant.revoke',$2,$3,$4,$5,$6,'pending',$7,$7,$7)
		ON CONFLICT(destination,idempotency_key) DO NOTHING`,
		platform.NewID(), aggregateType, event.AggregateID, event.AggregateVersion, payload,
		fmt.Sprintf("publication:%s:revoke:%s", event.ID, grantID), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) FailContentPublish(ctx context.Context, event publication.Event, assets []publication.PublishedAsset, detail string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := failEvent(ctx, tx, event, detail, now); err != nil {
		return err
	}
	for _, asset := range assets {
		if asset.AssetID == "" || asset.GrantID == "" {
			continue
		}
		payload, err := json.Marshal(publication.ContentUnpublishPayload{AssetID: asset.AssetID, GrantID: asset.GrantID, AggregateVersion: event.AggregateVersion})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
			VALUES($1,'asset-api','asset.grant.revoke','news',$2,$3,$4,$5,$6,'pending',$7,$7,$7)
			ON CONFLICT(destination,idempotency_key) DO NOTHING`,
			platform.NewID(), event.AggregateID, event.AggregateVersion, payload,
			fmt.Sprintf("publication:%s:revoke:%s", event.ID, asset.GrantID), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func failEvent(ctx context.Context, tx *sql.Tx, event publication.Event, detail string, now time.Time) error {
	var payload publication.PublishPayload
	_ = json.Unmarshal(event.Payload, &payload)
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='failed',claimed_until=NULL,last_error=$2,updated_at=$3 WHERE id=$1`, event.ID, detail, now); err != nil {
		return err
	}
	if strings.HasPrefix(event.EventType, "news.") {
		var contentID string
		var version int64
		nextStatus := "draft"
		if event.EventType == "news.unpublish.revoke_asset" {
			var contentPayload publication.ContentUnpublishPayload
			if err := json.Unmarshal(event.Payload, &contentPayload); err != nil {
				return err
			}
			contentID, version, nextStatus = contentPayload.ContentID, contentPayload.AggregateVersion, "unpublish_failed"
		} else {
			var contentPayload publication.ContentPublishPayload
			if err := json.Unmarshal(event.Payload, &contentPayload); err != nil {
				return err
			}
			contentID, version = contentPayload.ContentID, contentPayload.AggregateVersion
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status=$3,updated_at=$4 WHERE id=$1 AND version=$2`, contentID, version, nextStatus, now); err != nil {
			return err
		}
		return nil
	}
	if payload.WorkflowID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.publication_workflow SET status='failed',error_code='PUBLICATION_FAILED',error_detail=$2,updated_at=$3 WHERE id=$1`, payload.WorkflowID, detail, now); err != nil {
			return err
		}
		if event.EventType == "bulletin.unpublish.revoke_asset" {
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='unpublish_failed',updated_at=$3 WHERE id=$1 AND version=$2 AND status='unpublishing'`, payload.IssueID, payload.AggregateVersion, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='unpublish_failed',updated_at=$4 WHERE issue_id=$1 AND locale=$2 AND status='unpublishing' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_issue WHERE id=$1 AND version=$3)`, payload.IssueID, payload.Locale, payload.AggregateVersion, now); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='draft',updated_at=$3 WHERE id=$1 AND version=$2 AND status='publishing'`, payload.IssueID, payload.AggregateVersion, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='draft',updated_at=$4 WHERE issue_id=$1 AND locale=$2 AND status='publishing' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_issue WHERE id=$1 AND version=$3)`, payload.IssueID, payload.Locale, payload.AggregateVersion, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Repository) CompletePublish(ctx context.Context, event publication.Event, grantID, downloadURL string, now time.Time) error {
	var payload publication.PublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	delivered, err := eventDelivered(ctx, tx, event.ID)
	if err != nil {
		return err
	}
	if delivered {
		return nil
	}
	var issueDate, issueStatus, title, subtitle, versionStatus, retiringAssetID, retiringGrantID string
	var issueNumber sql.NullInt64
	var issueVersion int64
	if err := tx.QueryRowContext(ctx, `
		SELECT i.issue_number,i.issue_date::text,i.status,i.version,v.title,v.subtitle,v.status,
		       COALESCE(v.retiring_asset_id,''),COALESCE(v.retiring_grant_id,'')
		FROM hhc_web.bulletin_issue i
		JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.locale=$2
		WHERE i.id=$1 FOR UPDATE OF i,v`, payload.IssueID, payload.Locale).
		Scan(&issueNumber, &issueDate, &issueStatus, &issueVersion, &title, &subtitle, &versionStatus, &retiringAssetID, &retiringGrantID); err != nil {
		return err
	}
	if issueVersion != payload.AggregateVersion || issueStatus != "publishing" || versionStatus != "publishing" {
		return publication.ErrStalePublication
	}
	fileName := fmt.Sprintf("%s-%s.pdf", issueDate, title)
	var number *int
	if issueNumber.Valid {
		value := int(issueNumber.Int64)
		number = &value
		fileName = fmt.Sprintf("%d-%s.pdf", value, title)
	}
	public := bulletins.PublicBulletin{IssueNumber: number, IssueDate: issueDate, Locale: payload.Locale, Title: title, Subtitle: subtitle, DownloadURL: downloadURL + "?filename=" + url.QueryEscape(fileName), DownloadFileName: fileName, PublishedAt: now, Version: issueVersion}
	encoded, err := json.Marshal(public)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='published',published_at=$2,updated_at=$2 WHERE id=$1`, payload.IssueID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='published',public_grant_id=$3,retiring_asset_id=NULL,retiring_grant_id=NULL,published_at=$4,updated_at=$4 WHERE issue_id=$1 AND locale=$2`, payload.IssueID, payload.Locale, grantID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'bulletin_issue',$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(projection_key) DO UPDATE SET resource_id=EXCLUDED.resource_id,route_path=EXCLUDED.route_path,version=EXCLUDED.version,etag=EXCLUDED.etag,payload_json=EXCLUDED.payload_json,updated_at=EXCLUDED.updated_at`, fmt.Sprintf("bulletins:issue:%s:%s", payload.Locale, issueDate), payload.IssueID, payload.Locale, "/bulletins/"+issueDate, issueVersion, etag, encoded, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'bulletin_latest',$2,$3,'/bulletins/latest',$4,$5,$6,$7) ON CONFLICT(projection_key) DO UPDATE SET resource_id=EXCLUDED.resource_id,version=EXCLUDED.version,etag=EXCLUDED.etag,payload_json=EXCLUDED.payload_json,updated_at=EXCLUDED.updated_at WHERE COALESCE((hhc_web.public_projection.payload_json->>'issueNumber')::integer,0) <= COALESCE((EXCLUDED.payload_json->>'issueNumber')::integer,0)`, fmt.Sprintf("bulletins:latest:%s", payload.Locale), payload.IssueID, payload.Locale, issueVersion, etag, encoded, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.publication_workflow SET status='completed',updated_at=$2 WHERE id=$1`, payload.WorkflowID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	if retiringAssetID != "" && retiringAssetID != payload.AssetID {
		retirePayload, _ := json.Marshal(publication.UnpublishPayload{
			IssueID: payload.IssueID, Locale: payload.Locale, AssetID: retiringAssetID,
			GrantID: retiringGrantID, AggregateVersion: issueVersion,
		})
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
			VALUES($1,'asset-api','bulletin.asset.retire','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)
			ON CONFLICT(destination,idempotency_key) DO NOTHING`,
			platform.NewID(), payload.IssueID, issueVersion, retirePayload,
			fmt.Sprintf("bulletin:%s:%s:retire:%s", payload.IssueID, payload.Locale, retiringAssetID), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) CompleteUnpublish(ctx context.Context, event publication.Event, now time.Time) error {
	var payload publication.UnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	delivered, err := eventDelivered(ctx, tx, event.ID)
	if err != nil {
		return err
	}
	if delivered {
		return nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE hhc_web.bulletin_version
		SET status='unpublished',public_grant_id=NULL,retiring_asset_id=NULL,retiring_grant_id=NULL,updated_at=$4
		WHERE issue_id=$1 AND locale=$2 AND status='unpublishing'
		  AND EXISTS(SELECT 1 FROM hhc_web.bulletin_issue WHERE id=$1 AND version=$3)`,
		payload.IssueID, payload.Locale, payload.AggregateVersion, now)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows == 0 {
		if err != nil {
			return err
		}
		return publication.ErrStalePublication
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE hhc_web.bulletin_issue
		SET status=CASE WHEN EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND status='published') THEN 'published' ELSE 'unpublished' END,
		    updated_at=$3
		WHERE id=$1 AND version=$2`, payload.IssueID, payload.AggregateVersion, now)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows == 0 {
		if err != nil {
			return err
		}
		return publication.ErrStalePublication
	}
	if payload.WorkflowID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.publication_workflow SET status='completed',updated_at=$2 WHERE id=$1`, payload.WorkflowID, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) Complete(ctx context.Context, id string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, id, now)
	return err
}

func (r *Repository) EventDelivered(ctx context.Context, id string) (bool, error) {
	var delivered bool
	err := r.db.QueryRowContext(ctx, `SELECT status='delivered' FROM hhc_web.outbox_event WHERE id=$1`, id).Scan(&delivered)
	return delivered, err
}

func eventDelivered(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM hhc_web.outbox_event WHERE id=$1 FOR UPDATE`, id).Scan(&status); err != nil {
		return false, err
	}
	return status == "delivered", nil
}

func mapConflict(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
		return bulletins.ErrConflict
	}
	return err
}
