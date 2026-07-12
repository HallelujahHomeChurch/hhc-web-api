package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) CreateIssue(ctx context.Context, date, actor, idempotency string, now time.Time) (bulletins.Issue, error) {
	id := platform.NewID()
	_, err := r.db.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_issue(id,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,'draft',1,$3,$4,$4,$5,$5) ON CONFLICT(idempotency_key) DO NOTHING`, id, date, idempotency, actor, now)
	if err != nil {
		return bulletins.Issue{}, mapConflict(err)
	}
	var existingID, existingDate string
	if err := r.db.QueryRowContext(ctx, `SELECT id::text,issue_date::text FROM hhc_web.bulletin_issue WHERE idempotency_key=$1`, idempotency).Scan(&existingID, &existingDate); err != nil {
		return bulletins.Issue{}, err
	}
	if existingDate != date {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	return r.GetIssue(ctx, existingID)
}

func (r *Repository) ListIssues(ctx context.Context, page, size int, status string) (bulletins.Page, error) {
	args := []any{}
	where := ""
	if status != "" {
		args = append(args, status)
		where = " WHERE status=$1"
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM hhc_web.bulletin_issue"+where, args...).Scan(&total); err != nil {
		return bulletins.Page{}, err
	}
	args = append(args, size, (page-1)*size)
	limitPos, offsetPos := len(args)-1, len(args)
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT id::text,issue_date::text,status,version,created_by,updated_by,published_at,created_at,updated_at FROM hhc_web.bulletin_issue%s ORDER BY issue_date DESC LIMIT $%d OFFSET $%d`, where, limitPos, offsetPos), args...)
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
		issue.Versions, err = r.versions(ctx, issue.ID)
		if err != nil {
			return bulletins.Page{}, err
		}
		items = append(items, issue)
	}
	return bulletins.Page{Items: items, Page: page, PageSize: size, Total: total}, rows.Err()
}

func (r *Repository) GetIssue(ctx context.Context, id string) (bulletins.Issue, error) {
	issue, err := scanIssue(r.db.QueryRowContext(ctx, `SELECT id::text,issue_date::text,status,version,created_by,updated_by,published_at,created_at,updated_at FROM hhc_web.bulletin_issue WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	}
	if err != nil {
		return bulletins.Issue{}, err
	}
	issue.Versions, err = r.versions(ctx, id)
	return issue, err
}

type scanner interface{ Scan(...any) error }

func scanIssue(row scanner) (bulletins.Issue, error) {
	var v bulletins.Issue
	var published sql.NullTime
	err := row.Scan(&v.ID, &v.IssueDate, &v.Status, &v.Version, &v.CreatedBy, &v.UpdatedBy, &published, &v.CreatedAt, &v.UpdatedAt)
	if published.Valid {
		value := published.Time
		v.PublishedAt = &value
	}
	return v, err
}
func (r *Repository) versions(ctx context.Context, id string) ([]bulletins.Version, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id::text,issue_id::text,locale,title,pdf_asset_id,pdf_file_name,status,version,published_at,created_at,updated_at FROM hhc_web.bulletin_version WHERE issue_id=$1 ORDER BY locale`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []bulletins.Version{}
	for rows.Next() {
		var v bulletins.Version
		var published sql.NullTime
		if err := rows.Scan(&v.ID, &v.IssueID, &v.Locale, &v.Title, &v.PDFAssetID, &v.PDFFileName, &v.Status, &v.Version, &published, &v.CreatedAt, &v.UpdatedAt); err != nil {
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
	if err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	if current != expected {
		return bulletins.Issue{}, bulletins.ErrPrecondition
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_version(id,issue_id,locale,title,pdf_asset_id,pdf_file_name,status,version,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'draft',1,$7,$7,$8,$8) ON CONFLICT(issue_id,locale) DO UPDATE SET title=EXCLUDED.title,pdf_asset_id=EXCLUDED.pdf_asset_id,pdf_file_name=EXCLUDED.pdf_file_name,status='draft',version=hhc_web.bulletin_version.version+1,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`, platform.NewID(), id, input.Locale, input.Title, input.PDFAssetID, input.PDFFileName, actor, now)
	if err != nil {
		return bulletins.Issue{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='draft',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, id, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return r.GetIssue(ctx, id)
}

func (r *Repository) StartPublish(ctx context.Context, id, locale string, expected int64, actor string, now time.Time) (bulletins.Workflow, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Workflow{}, err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Workflow{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Workflow{}, err
	}
	if current != expected {
		return bulletins.Workflow{}, bulletins.ErrPrecondition
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
	if err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	if current != expected {
		return bulletins.Issue{}, bulletins.ErrPrecondition
	}
	var assetID, date string
	if err := tx.QueryRowContext(ctx, `SELECT v.pdf_asset_id,i.issue_date::text FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id WHERE v.issue_id=$1 AND v.locale=$2 AND v.status='published' FOR UPDATE`, id, locale).Scan(&assetID, &date); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotPublishable
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	next := current + 1
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='unpublished',version=version+1,updated_by=$3,updated_at=$4 WHERE issue_id=$1 AND locale=$2`, id, locale, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='unpublished',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, next, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE projection_key=$1 OR (projection_key=$2 AND resource_id=$3)`, fmt.Sprintf("bulletins:issue:%s:%s", locale, date), fmt.Sprintf("bulletins:latest:%s", locale), id); err != nil {
		return bulletins.Issue{}, err
	}
	payload, _ := json.Marshal(map[string]any{"issueId": id, "locale": locale, "assetId": assetID, "aggregateVersion": next})
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'asset-api','bulletin.unpublish.revoke_asset','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)`, platform.NewID(), id, next, payload, fmt.Sprintf("bulletin:%s:%s:unpublish:v%d", id, locale, next), now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return r.GetIssue(ctx, id)
}

func (r *Repository) GetPublicLatest(ctx context.Context, locale string) (bulletins.PublicBulletin, error) {
	return r.publicProjection(ctx, fmt.Sprintf("bulletins:latest:%s", locale))
}
func (r *Repository) GetPublicByDate(ctx context.Context, date, locale string) (bulletins.PublicBulletin, error) {
	return r.publicProjection(ctx, fmt.Sprintf("bulletins:issue:%s:%s", locale, date))
}
func (r *Repository) publicProjection(ctx context.Context, key string) (bulletins.PublicBulletin, error) {
	var payload []byte
	err := r.db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE projection_key=$1`, key).Scan(&payload)
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
func (r *Repository) ListPublic(ctx context.Context, locale string, page, size int) (bulletins.PublicPage, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='bulletin_issue' AND locale=$1`, locale).Scan(&total); err != nil {
		return bulletins.PublicPage{}, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE resource_type='bulletin_issue' AND locale=$1 ORDER BY payload_json->>'issueDate' DESC LIMIT $2 OFFSET $3`, locale, size, (page-1)*size)
	if err != nil {
		return bulletins.PublicPage{}, err
	}
	defer rows.Close()
	items := []bulletins.PublicBulletin{}
	for rows.Next() {
		var payload []byte
		var item bulletins.PublicBulletin
		if err := rows.Scan(&payload); err != nil {
			return bulletins.PublicPage{}, err
		}
		if err := json.Unmarshal(payload, &item); err != nil {
			return bulletins.PublicPage{}, err
		}
		items = append(items, item)
	}
	return bulletins.PublicPage{Items: items, Page: page, PageSize: size, Total: total}, rows.Err()
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
