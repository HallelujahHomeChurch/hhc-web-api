package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

type Repository struct {
	inboxPublicationsAfter                                 time.Time
	db                                                     *sql.DB
	enableFiveLocaleBulletinNotificationsAfterFluentReview bool
	auditOutbox                                            *AuditOutbox
	automaticBulletinExtraction                            bool
	llmBulletinExtraction                                  bool
}

func New(db *sql.DB, enableFiveLocaleBulletinNotificationsAfterFluentReview ...bool) *Repository {
	enabled := false
	if len(enableFiveLocaleBulletinNotificationsAfterFluentReview) > 0 {
		enabled = enableFiveLocaleBulletinNotificationsAfterFluentReview[0]
	}
	return &Repository{db: db, enableFiveLocaleBulletinNotificationsAfterFluentReview: enabled}
}

func (r *Repository) WithAudit(outbox *AuditOutbox) *Repository {
	r.auditOutbox = outbox
	return r
}

func (r *Repository) WithAutomaticBulletinExtraction(enabled bool) *Repository {
	r.automaticBulletinExtraction = enabled
	return r
}

func (r *Repository) WithLLMBulletinExtraction(enabled bool) *Repository {
	r.llmBulletinExtraction = enabled
	return r
}

func (r *Repository) CreateIssue(ctx context.Context, number int, date, actor, idempotency string, now time.Time) (bulletins.Issue, error) {
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Issue{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	id := platform.NewID()
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,'draft',1,$4,$5,$5,$6,$6) ON CONFLICT(idempotency_key) DO NOTHING`, id, number, date, idempotency, actor, now)
	if err != nil {
		return bulletins.Issue{}, mapConflict(err)
	}
	created, _ := result.RowsAffected()
	var existingID, existingDate string
	var existingNumber sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT id::text,issue_number,issue_date::text FROM hhc_web.bulletin_issue WHERE idempotency_key=$1`, idempotency).Scan(&existingID, &existingNumber, &existingDate); err != nil {
		return bulletins.Issue{}, err
	}
	if !existingNumber.Valid || int(existingNumber.Int64) != number || existingDate != date {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	issue, err := loadIssue(ctx, tx, existingID)
	if err == nil && created == 1 {
		err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.create", existingID, actor, now)
	}
	if err != nil {
		return bulletins.Issue{}, err
	}
	return issue, tx.Commit()
}

func (r *Repository) ListIssues(ctx context.Context, page, size int, status, query, sort, direction string) (bulletins.Page, error) {
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
	order := map[string]string{"issueNumber": "i.issue_number", "date": "i.issue_date", "status": "i.status", "updated": "i.updated_at", "title": "(SELECT title FROM hhc_web.bulletin_version WHERE issue_id=i.id ORDER BY (locale='zh-Hant') DESC,locale LIMIT 1)", "languages": "(SELECT count(*) FROM hhc_web.bulletin_version WHERE issue_id=i.id)"}[sort]
	direction = strings.ToUpper(direction)
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT i.id::text,i.issue_number,i.issue_date::text,i.status,i.version,i.created_by,i.updated_by,i.published_at,i.created_at,i.updated_at FROM hhc_web.bulletin_issue i%s ORDER BY %s %s NULLS LAST,i.id %s LIMIT $%d OFFSET $%d`, where, order, direction, direction, limitPos, offsetPos), args...)
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
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Issue{}, err
	}
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
	if err := r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.update", issue.ID, actor, now); err != nil {
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
		SELECT v.id::text,v.issue_id::text,v.series,v.locale,v.title,v.subtitle,v.pdf_asset_id,v.pdf_file_name,
		       COALESCE(v.public_grant_id,''),v.status,COALESCE(w.status,''),COALESCE(w.error_detail,''),
		       v.version,v.published_at,v.created_at,v.updated_at
		FROM hhc_web.bulletin_version v
		LEFT JOIN LATERAL (
			SELECT status,error_detail
			FROM hhc_web.publication_workflow
			WHERE resource_id=v.issue_id AND series=v.series AND locale=v.locale
			ORDER BY created_at DESC
			LIMIT 1
		) w ON true
		WHERE v.issue_id IN (%s)
		ORDER BY v.issue_id,v.series,v.locale`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []bulletins.Version{}
	for rows.Next() {
		var v bulletins.Version
		var published sql.NullTime
		if err := rows.Scan(&v.ID, &v.IssueID, &v.Series, &v.Locale, &v.Title, &v.Subtitle, &v.PDFAssetID, &v.PDFFileName, &v.PublicGrantID, &v.Status, &v.WorkflowStatus, &v.WorkflowError, &v.Version, &published, &v.CreatedAt, &v.UpdatedAt); err != nil {
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
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Issue{}, err
	}
	edition, ok := bulletins.NormalizeBulletinEdition(input.Series, input.Locale)
	if !ok {
		return bulletins.Issue{}, bulletins.ErrInvalid
	}
	input.Series = string(edition.Series)
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
	err = tx.QueryRowContext(ctx, `SELECT status FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series=$2 AND locale=$3`, id, input.Series, input.Locale).Scan(&existingStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, err
	}
	if existingStatus == "unpublishing" || existingStatus == "unpublish_failed" {
		return bulletins.Issue{}, bulletins.ErrNotPublishable
	}
	var versionID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO hhc_web.bulletin_version(id,issue_id,series,locale,title,subtitle,pdf_asset_id,pdf_file_name,status,version,created_by,updated_by,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,'draft',1,$9,$9,$10,$10)
		ON CONFLICT(issue_id,series,locale) DO UPDATE SET
			retiring_asset_id=CASE WHEN hhc_web.bulletin_version.status='published' THEN hhc_web.bulletin_version.pdf_asset_id ELSE hhc_web.bulletin_version.retiring_asset_id END,
			retiring_grant_id=CASE WHEN hhc_web.bulletin_version.status='published' THEN hhc_web.bulletin_version.public_grant_id ELSE hhc_web.bulletin_version.retiring_grant_id END,
			title=EXCLUDED.title,subtitle=EXCLUDED.subtitle,pdf_asset_id=EXCLUDED.pdf_asset_id,pdf_file_name=EXCLUDED.pdf_file_name,status='draft',
			version=hhc_web.bulletin_version.version+1,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at
		RETURNING id::text`, platform.NewID(), id, input.Series, input.Locale, input.Title, input.Subtitle, input.PDFAssetID, input.PDFFileName, actor, now).Scan(&versionID)
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
	if err := r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.version.update", versionID, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if r.automaticBulletinExtraction && issue.IssueNumber != nil && *issue.IssueNumber >= 1739 && input.Series == "general" && input.Locale == "zh-Hant" {
		if err := enqueueUploadExtraction(ctx, tx, issue, input.PDFAssetID, actor, now); err != nil {
			return bulletins.Issue{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return issue, nil
}

func (r *Repository) UpdateVersion(ctx context.Context, id, series, locale string, expected int64, title, subtitle, actor string, now time.Time) (bulletins.Issue, error) {
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Issue{}, err
	}
	if _, ok := bulletins.NormalizeBulletinEdition(series, locale); !ok {
		return bulletins.Issue{}, bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	if err := lockMutableIssue(ctx, tx, id, expected); err != nil {
		return bulletins.Issue{}, err
	}
	var versionID string
	err = tx.QueryRowContext(ctx, `UPDATE hhc_web.bulletin_version SET title=$4,subtitle=$5,status='draft',version=version+1,updated_by=$6,updated_at=$7 WHERE issue_id=$1 AND series=$2 AND locale=$3 AND status IN ('draft','unpublished') AND COALESCE(public_grant_id,'')='' AND COALESCE(retiring_grant_id,'')='' RETURNING id::text`, id, series, locale, title, subtitle, actor, now).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	if err != nil {
		return bulletins.Issue{}, err
	}
	if err := r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.version.update", versionID, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	return finishIssueDraft(ctx, tx, id, actor, now)
}

func (r *Repository) DeleteVersion(ctx context.Context, id, series, locale string, expected int64, actor string, now time.Time) (bulletins.Issue, error) {
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Issue{}, err
	}
	if _, ok := bulletins.NormalizeBulletinEdition(series, locale); !ok {
		return bulletins.Issue{}, bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	if err := lockMutableIssue(ctx, tx, id, expected); err != nil {
		return bulletins.Issue{}, err
	}
	var versionID, assetID, status, publicGrantID, retiringGrantID string
	if err := tx.QueryRowContext(ctx, `SELECT id::text,pdf_asset_id,status,COALESCE(public_grant_id,''),COALESCE(retiring_grant_id,'') FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series=$2 AND locale=$3 FOR UPDATE`, id, series, locale).Scan(&versionID, &assetID, &status, &publicGrantID, &retiringGrantID); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	if err := rejectOnlineHistoryDeletion(ctx, tx, id, series, locale); err != nil {
		return bulletins.Issue{}, err
	}
	if status != "draft" && status != "unpublished" || publicGrantID != "" || retiringGrantID != "" {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series=$2 AND locale=$3`, id, series, locale); err != nil {
		return bulletins.Issue{}, err
	}
	if err := enqueueAssetDeletes(ctx, tx, "bulletin", id, expected, []string{assetID}, now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.version.delete", versionID, actor, now); err != nil {
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
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Issue{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	var current int64
	var status, currentDate string
	var currentNumber sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT version,status,issue_number,issue_date::text FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&current, &status, &currentNumber, &currentDate); errors.Is(err, sql.ErrNoRows) {
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
	for index := range snapshot.Versions {
		edition, ok := bulletins.NormalizeBulletinEdition(snapshot.Versions[index].Series, snapshot.Versions[index].Locale)
		if !ok {
			return bulletins.Issue{}, bulletins.ErrInvalid
		}
		snapshot.Versions[index].Series = string(edition.Series)
	}
	metadataChanged := currentDate != snapshot.IssueDate || currentNumber.Valid != (snapshot.IssueNumber != nil)
	if currentNumber.Valid && snapshot.IssueNumber != nil {
		metadataChanged = metadataChanged || currentNumber.Int64 != int64(*snapshot.IssueNumber)
	}
	if metadataChanged {
		var hasPublishedVersion bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version WHERE issue_id=$1)`, id).Scan(&hasPublishedVersion); err != nil {
			return bulletins.Issue{}, err
		}
		if hasPublishedVersion {
			return bulletins.Issue{}, bulletins.ErrConflict
		}
	}
	args := []any{id}
	pairs := make([]string, len(snapshot.Versions))
	for index, version := range snapshot.Versions {
		seriesParam := len(args) + 1
		args = append(args, version.Series)
		localeParam := len(args) + 1
		args = append(args, version.Locale)
		pairs[index] = fmt.Sprintf("(series=$%d AND locale=$%d)", seriesParam, localeParam)
	}
	missingClause := ""
	if len(pairs) > 0 {
		missingClause = " AND NOT (" + strings.Join(pairs, " OR ") + ")"
	}
	var hasPublicVersion bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1`+missingClause+` AND status IN ('published','unpublishing','unpublish_failed'))`, args...).Scan(&hasPublicVersion); err != nil {
		return bulletins.Issue{}, err
	}
	if hasPublicVersion {
		return bulletins.Issue{}, bulletins.ErrConflict
	}
	var hasOnlineHistory bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_version v WHERE issue_id=$1`+missingClause+` AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_document d WHERE d.issue_id=v.issue_id AND d.series=v.series AND d.content_locale=v.locale))`, args...).Scan(&hasOnlineHistory); err != nil {
		return bulletins.Issue{}, err
	}
	if hasOnlineHistory {
		return bulletins.Issue{}, bulletins.ErrOnlineDocumentExists
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
			INSERT INTO hhc_web.bulletin_version(id,issue_id,series,locale,title,subtitle,pdf_asset_id,pdf_file_name,status,version,created_by,updated_by,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'draft',1,$9,$9,$10,$10)
			ON CONFLICT(issue_id,series,locale) DO UPDATE SET
				retiring_asset_id=CASE WHEN hhc_web.bulletin_version.status='published' THEN hhc_web.bulletin_version.pdf_asset_id ELSE hhc_web.bulletin_version.retiring_asset_id END,
				retiring_grant_id=CASE WHEN hhc_web.bulletin_version.status='published' THEN hhc_web.bulletin_version.public_grant_id ELSE hhc_web.bulletin_version.retiring_grant_id END,
				title=EXCLUDED.title,subtitle=EXCLUDED.subtitle,pdf_asset_id=EXCLUDED.pdf_asset_id,pdf_file_name=EXCLUDED.pdf_file_name,status='draft',
				version=hhc_web.bulletin_version.version+1,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
			versionID, id, version.Series, version.Locale, version.Title, version.Subtitle, version.PDFAssetID, version.PDFFileName, actor, now); err != nil {
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
	if err := r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.restore", restored.ID, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return restored, nil
}

func insertBulletinRevision(ctx context.Context, tx *sql.Tx, issue bulletins.Issue, actor string, now time.Time) error {
	if err := invalidateOnlineMetadata(ctx, tx, issue, actor, now); err != nil {
		return err
	}
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

func (r *Repository) StartPublish(ctx context.Context, id, series, locale string, expected int64, notifySubscribers bool, actor string, now time.Time) (bulletins.Workflow, error) {
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Workflow{}, err
	}
	if _, ok := bulletins.NormalizeBulletinEdition(series, locale); !ok {
		return bulletins.Workflow{}, bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Workflow{}, err
	}
	defer tx.Rollback()
	var canonicalID string
	var current int64
	var issueStatus string
	if err := tx.QueryRowContext(ctx, `SELECT id::text,version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&canonicalID, &current, &issueStatus); errors.Is(err, sql.ErrNoRows) {
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
	if err := tx.QueryRowContext(ctx, `SELECT pdf_asset_id FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series=$2 AND locale=$3 FOR UPDATE`, canonicalID, series, locale).Scan(&assetID); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Workflow{}, bulletins.ErrNotPublishable
	} else if err != nil {
		return bulletins.Workflow{}, err
	}
	next := current + 1
	workflow := bulletins.Workflow{ID: platform.NewID(), Status: "waiting_asset_scan", AggregateVersion: next, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='publishing',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, canonicalID, next, actor, now); err != nil {
		return bulletins.Workflow{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='publishing',version=version+1,updated_by=$4,updated_at=$5 WHERE issue_id=$1 AND series=$2 AND locale=$3`, canonicalID, series, locale, actor, now); err != nil {
		return bulletins.Workflow{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.publication_workflow(id,workflow_type,resource_type,resource_id,series,locale,aggregate_version,asset_id,status,created_by,created_at,updated_at) VALUES($1,'bulletin_publish','bulletin',$2,$3,$4,$5,$6,$7,$8,$9,$9)`, workflow.ID, canonicalID, series, locale, next, assetID, workflow.Status, actor, now); err != nil {
		return bulletins.Workflow{}, err
	}
	payload, _ := json.Marshal(publication.PublishPayload{WorkflowID: workflow.ID, IssueID: canonicalID, Series: series, Locale: locale, AssetID: assetID, AggregateVersion: next, NotifySubscribers: notifySubscribers, ActorID: actor})
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'asset-api','bulletin.publish.ensure_asset','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)`, platform.NewID(), canonicalID, next, payload, fmt.Sprintf("bulletin:%s:%s:%s:publish:v%d", canonicalID, series, locale, next), now); err != nil {
		return bulletins.Workflow{}, err
	}
	if err := r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.publish", canonicalID, actor, now); err != nil {
		return bulletins.Workflow{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Workflow{}, err
	}
	return workflow, nil
}

func (r *Repository) Unpublish(ctx context.Context, id, series, locale string, expected int64, actor string, now time.Time) (bulletins.Issue, error) {
	return r.unpublishBulletin(ctx, id, series, locale, expected, nil, actor, now)
}

func (r *Repository) UnpublishWithOnline(ctx context.Context, id, series, locale string, expected, onlineVersion int64, actor string, now time.Time) (bulletins.Issue, error) {
	if series != "general" || locale != "zh-Hant" || onlineVersion < 1 {
		return bulletins.Issue{}, bulletins.ErrInvalid
	}
	return r.unpublishBulletin(ctx, id, series, locale, expected, &onlineVersion, actor, now)
}

func (r *Repository) unpublishBulletin(ctx context.Context, id, series, locale string, expected int64, onlineVersion *int64, actor string, now time.Time) (bulletins.Issue, error) {
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Issue{}, err
	}
	if _, ok := bulletins.NormalizeBulletinEdition(series, locale); !ok {
		return bulletins.Issue{}, bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	var canonicalID string
	var current int64
	var issueStatus string
	if err := tx.QueryRowContext(ctx, `SELECT id::text,version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&canonicalID, &current, &issueStatus); errors.Is(err, sql.ErrNoRows) {
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
	var assetID, grantID string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(NULLIF(v.retiring_asset_id,''),v.pdf_asset_id),COALESCE(v.public_grant_id,'')
		FROM hhc_web.bulletin_version v
		JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id
		WHERE v.issue_id=$1 AND v.series=$2 AND v.locale=$3 AND v.status IN ('published','draft','unpublish_failed')
		FOR UPDATE`, canonicalID, series, locale).Scan(&assetID, &grantID); errors.Is(err, sql.ErrNoRows) {
		return bulletins.Issue{}, bulletins.ErrNotPublishable
	} else if err != nil {
		return bulletins.Issue{}, err
	}
	if onlineVersion != nil {
		var documentID string
		var currentOnline int64
		var published sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT id::text,version,published_revision FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series=$2 AND content_locale=$3 FOR UPDATE`, canonicalID, series, locale).Scan(&documentID, &currentOnline, &published)
		if errors.Is(err, sql.ErrNoRows) {
			return bulletins.Issue{}, bulletins.ErrNotFound
		}
		if err != nil {
			return bulletins.Issue{}, err
		}
		if currentOnline != *onlineVersion {
			return bulletins.Issue{}, bulletins.ErrPrecondition
		}
		if published.Valid {
			if err = r.clearOnlinePublication(ctx, tx, documentID, actor, now); err != nil {
				return bulletins.Issue{}, err
			}
		}
	}
	next := current + 1
	workflowID := platform.NewID()
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='unpublishing',version=version+1,updated_by=$4,updated_at=$5 WHERE issue_id=$1 AND series=$2 AND locale=$3`, canonicalID, series, locale, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status='unpublishing',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, canonicalID, next, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1 AND series=$2 AND locale=$3`, canonicalID, series, locale); err != nil {
		return bulletins.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.publication_workflow(id,workflow_type,resource_type,resource_id,series,locale,aggregate_version,asset_id,status,created_by,created_at,updated_at) VALUES($1,'bulletin_unpublish','bulletin',$2,$3,$4,$5,$6,'revoke_pending',$7,$8,$8)`, workflowID, canonicalID, series, locale, next, assetID, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	payload, _ := json.Marshal(publication.UnpublishPayload{ActorID: actor, WorkflowID: workflowID, IssueID: canonicalID, Series: series, Locale: locale, AssetID: assetID, GrantID: grantID, AggregateVersion: next})
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'asset-api','bulletin.unpublish.revoke_asset','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)`, platform.NewID(), canonicalID, next, payload, fmt.Sprintf("bulletin:%s:%s:%s:unpublish:v%d", canonicalID, series, locale, next), now); err != nil {
		return bulletins.Issue{}, err
	}
	issue, err := loadIssue(ctx, tx, canonicalID)
	if err != nil {
		return bulletins.Issue{}, err
	}
	if err := r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.unpublish", canonicalID, actor, now); err != nil {
		return bulletins.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return bulletins.Issue{}, err
	}
	return issue, nil
}

func (r *Repository) DeleteIssue(ctx context.Context, id string, expected int64, actor string, now time.Time) error {
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var canonicalID string
	var current int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT id::text,version,status FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, id).Scan(&canonicalID, &current, &status); errors.Is(err, sql.ErrNoRows) {
		return bulletins.ErrNotFound
	} else if err != nil {
		return err
	}
	if current != expected {
		return bulletins.ErrPrecondition
	}
	if err := rejectOnlineHistoryDeletion(ctx, tx, canonicalID, "", ""); err != nil {
		return err
	}
	if status == "publishing" || status == "published" || status == "unpublishing" || status == "unpublish_failed" {
		return bulletins.ErrConflict
	}
	assetIDs, err := bulletinAssetIDs(ctx, tx, canonicalID)
	if err != nil {
		return err
	}
	if err := insertDeleteAudit(ctx, tx, "bulletin", canonicalID, current, actor, assetIDs, now); err != nil {
		return err
	}
	if err := enqueueAssetDeletes(ctx, tx, "bulletin", canonicalID, current, assetIDs, now); err != nil {
		return err
	}
	if result, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, canonicalID); err != nil {
		return err
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		return bulletins.ErrNotFound
	}
	if err := r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.delete", canonicalID, actor, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) validateBulletinAudit(ctx context.Context, actor string) error {
	if r.auditOutbox == nil {
		return nil
	}
	provenance, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || provenance.ActorID != actor || !auditclient.ValidActorRequest(provenance.ActorID, provenance.RequestID) {
		return bulletins.ErrAuditUnavailable
	}
	return nil
}

func (r *Repository) enqueueBulletinAudit(ctx context.Context, tx *sql.Tx, action, resourceID, actor string, now time.Time) error {
	if r.auditOutbox == nil {
		return nil
	}
	provenance, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || provenance.ActorID != actor {
		return bulletins.ErrAuditUnavailable
	}
	event, err := auditclient.NewEvent(action, resourceID, actor, provenance.RequestID, now, auditclient.Metadata{})
	if err == nil {
		_, err = r.auditOutbox.EnqueueTx(ctx, tx, event, now)
	}
	if err != nil {
		return bulletins.ErrAuditUnavailable
	}
	return nil
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

func (r *Repository) Claim(ctx context.Context, now time.Time, lease time.Duration) (publication.Event, bool, error) {
	var event publication.Event
	err := r.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id FROM hhc_web.outbox_event
			WHERE destination<>'engagement-inbox' AND ((status='pending' AND next_attempt_at <= $1)
			   OR (status='processing' AND claimed_until <= $1))
			ORDER BY next_attempt_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE hhc_web.outbox_event e
		SET status='processing',attempts=e.attempts+1,claimed_until=$2,updated_at=$1
		FROM candidate
		WHERE e.id=candidate.id
		RETURNING e.id::text,e.event_type,e.aggregate_id::text,e.aggregate_version,e.payload_json,e.attempts,e.created_at,e.claimed_until,
			COALESCE(NULLIF(e.payload_json->>'actorId',''),CASE WHEN e.event_type='news.unpublish.revoke_asset' THEN COALESCE((
				SELECT updated_by FROM hhc_web.content_entry
				WHERE id=e.aggregate_id AND version=e.aggregate_version AND status='unpublishing'
			 ),'') WHEN e.event_type='asset.grant.revoke' THEN COALESCE((
 SELECT COALESCE(NULLIF(p.payload_json->>'actorId',''),(
 SELECT m.created_by FROM hhc_web.page_publication_manifest m
 WHERE p.event_type='home.publish.ensure_asset' AND m.page_id=p.aggregate_id
 AND m.page_version=p.aggregate_version AND m.manifest_sha256=p.payload_json->>'manifestSha256'
 )) FROM hhc_web.outbox_event p
 WHERE p.id::text=split_part(e.idempotency_key,':',2)
 AND e.idempotency_key LIKE 'publication:%:revoke:%'
 AND p.aggregate_id=e.aggregate_id AND p.aggregate_version=e.aggregate_version
 AND p.destination='asset-api'
 AND p.event_type IN ('news.publish.ensure_asset','home.publish.ensure_asset','bulletin.publish.ensure_asset')
 ),(
 SELECT COALESCE(NULLIF(p.payload_json->>'actorId',''),(
 SELECT m.created_by FROM hhc_web.page_publication_manifest m
 WHERE p.event_type='home.publish.ensure_asset' AND m.page_id=p.aggregate_id
 AND m.page_version=p.aggregate_version AND m.manifest_sha256=p.payload_json->>'manifestSha256'
 )) FROM hhc_web.outbox_event p
 WHERE e.aggregate_type IN ('news','home') AND e.destination='asset-api'
 AND e.idempotency_key=e.aggregate_type||':'||e.aggregate_id::text||':revoke:'||(e.payload_json->>'grantId')
 AND e.payload_json->>'contentId'=e.aggregate_id::text
 AND e.payload_json->>'aggregateVersion'=e.aggregate_version::text
 AND p.destination='asset-api' AND p.status='delivered'
 AND p.idempotency_key=e.aggregate_type||':'||e.aggregate_id::text||':publish:v'||e.aggregate_version::text
 AND p.event_type=e.aggregate_type||'.publish.ensure_asset' AND p.aggregate_type=e.aggregate_type
 AND p.aggregate_id=e.aggregate_id AND p.aggregate_version=e.aggregate_version
 AND p.payload_json->>'contentId'=e.aggregate_id::text
 AND p.payload_json->>'aggregateVersion'=e.aggregate_version::text
 AND NOT EXISTS (SELECT 1 FROM hhc_web.news_item n WHERE n.public_grant_id=e.payload_json->>'grantId' OR n.home_public_grant_id=e.payload_json->>'grantId')
 AND NOT EXISTS (SELECT 1 FROM hhc_web.page_item pg WHERE pg.banner_public_grant_id=e.payload_json->>'grantId')
 AND NOT EXISTS (SELECT 1 FROM hhc_web.content_published_asset a WHERE a.grant_id=e.payload_json->>'grantId')
 ),'') WHEN e.event_type='bulletin.unpublish.revoke_asset' THEN COALESCE((SELECT created_by FROM hhc_web.publication_workflow WHERE id::text=e.payload_json->>'workflowId' AND resource_id=e.aggregate_id AND aggregate_version=e.aggregate_version AND workflow_type='bulletin_unpublish'),'') ELSE '' END)`, now, now.Add(lease)).Scan(
		&event.ID, &event.EventType, &event.AggregateID, &event.AggregateVersion, &event.Payload, &event.Attempts, &event.CreatedAt, &event.ClaimedUntil, &event.ActorID,
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
	if event.EventType == "news.summary.generate" {
		return r.FinishNewsSummary(ctx, event, "", "AI_FAILED", time.Time{}, now)
	}
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
	payload, err := json.Marshal(publication.ContentUnpublishPayload{ActorID: event.ActorID, RequestID: event.ID, AssetID: assetID, GrantID: grantID, AggregateVersion: event.AggregateVersion})
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
	aggregateType := "news"
	if strings.HasPrefix(event.EventType, "home.") {
		aggregateType = "home"
	}
	for _, asset := range assets {
		if asset.AssetID == "" || asset.GrantID == "" {
			continue
		}
		payload, err := json.Marshal(publication.ContentUnpublishPayload{ActorID: event.ActorID, RequestID: event.ID, AssetID: asset.AssetID, GrantID: asset.GrantID, AggregateVersion: event.AggregateVersion})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
			VALUES($1,'asset-api','asset.grant.revoke',$2,$3,$4,$5,$6,'pending',$7,$7,$7)
			ON CONFLICT(destination,idempotency_key) DO NOTHING`,
			platform.NewID(), aggregateType, event.AggregateID, event.AggregateVersion, payload,
			fmt.Sprintf("publication:%s:revoke:%s", event.ID, asset.GrantID), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func failEvent(ctx context.Context, tx *sql.Tx, event publication.Event, detail string, now time.Time) error {
	var payload publication.PublishPayload
	_ = json.Unmarshal(event.Payload, &payload)
	if edition, ok := bulletins.NormalizeBulletinEdition(payload.Series, payload.Locale); ok {
		payload.Series = string(edition.Series)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='failed',claimed_until=NULL,last_error=$2,updated_at=$3 WHERE id=$1`, event.ID, detail, now); err != nil {
		return err
	}
	if event.EventType == "statement.notification.queue" {
		var ref struct {
			BatchID string `json:"batchId"`
		}
		if err := json.Unmarshal(event.Payload, &ref); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.statement_notification_batch SET status=CASE WHEN cancelled_at IS NOT NULL THEN 'cancelled' ELSE 'failed' END,updated_at=$2 WHERE id=$1`, ref.BatchID, now)
		return err
	}
	if event.EventType == "bulletin.notification.queue" {
		var notification publication.BulletinNotificationPayload
		if err := json.Unmarshal(event.Payload, &notification); err != nil {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET notification_status='failed',notification_error_code='NOTIFICATION_QUEUE_FAILED',updated_at=$2 WHERE id=$1 AND notification_status='pending'`, notification.IssueID, now)
		return err
	}
	if strings.HasPrefix(event.EventType, "news.") || strings.HasPrefix(event.EventType, "home.") {
		var contentID string
		var version int64
		var manifestSHA string
		nextStatus := failedContentStatus(event.EventType)
		if event.EventType == "news.unpublish.revoke_asset" {
			var contentPayload publication.ContentUnpublishPayload
			if err := json.Unmarshal(event.Payload, &contentPayload); err != nil {
				return err
			}
			contentID, version = contentPayload.ContentID, contentPayload.AggregateVersion
		} else {
			var contentPayload publication.ContentPublishPayload
			if err := json.Unmarshal(event.Payload, &contentPayload); err != nil {
				return err
			}
			contentID, version, manifestSHA = contentPayload.ContentID, contentPayload.AggregateVersion, contentPayload.ManifestSHA256
		}
		expectedStatus := content.StatusPublishing
		if event.EventType == "news.unpublish.revoke_asset" {
			expectedStatus = content.StatusUnpublishing
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status=$3,updated_at=$4 WHERE id=$1 AND version=$2 AND status=$5`, contentID, version, nextStatus, now, expectedStatus); err != nil {
			return err
		}
		if event.EventType == "home.publish.ensure_asset" {
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_publication_manifest SET publication_status='failed' WHERE page_id=$1 AND page_version=$2 AND manifest_sha256=$3 AND publication_status='pending'`, contentID, version, manifestSHA); err != nil {
				return err
			}
		}
		return nil
	}
	if payload.WorkflowID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.publication_workflow SET status='failed',error_code='PUBLICATION_FAILED',error_detail=$2,updated_at=$3 WHERE id=$1`, payload.WorkflowID, detail, now); err != nil {
			return err
		}
		if event.EventType == "bulletin.unpublish.revoke_asset" {
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='unpublish_failed',updated_at=$5 WHERE issue_id=$1 AND series=$2 AND locale=$3 AND status='unpublishing' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_issue WHERE id=$1 AND version=$4)`, payload.IssueID, payload.Series, payload.Locale, payload.AggregateVersion, now); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='draft',updated_at=$5 WHERE issue_id=$1 AND series=$2 AND locale=$3 AND status='publishing' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_issue WHERE id=$1 AND version=$4)`, payload.IssueID, payload.Series, payload.Locale, payload.AggregateVersion, now); err != nil {
				return err
			}
		}
		if err := recalculateBulletinIssueStatus(ctx, tx, payload.IssueID, payload.AggregateVersion, now); err != nil {
			return err
		}
	}
	return nil
}

func failedContentStatus(eventType string) string {
	if eventType == "news.unpublish.revoke_asset" {
		return content.StatusUnpublishFailed
	}
	return content.StatusPublishFailed
}

func (r *Repository) CompletePublish(ctx context.Context, event publication.Event, _, _ string, now time.Time) error {
	var payload publication.PublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	edition, ok := bulletins.NormalizeBulletinEdition(payload.Series, payload.Locale)
	if !ok {
		return bulletins.ErrInvalid
	}
	payload.Series = string(edition.Series)
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
		JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=$2 AND v.locale=$3
		WHERE i.id=$1 FOR UPDATE OF i,v`, payload.IssueID, payload.Series, payload.Locale).
		Scan(&issueNumber, &issueDate, &issueStatus, &issueVersion, &title, &subtitle, &versionStatus, &retiringAssetID, &retiringGrantID); err != nil {
		return err
	}
	if issueVersion != payload.AggregateVersion || issueStatus != "publishing" || versionStatus != "publishing" {
		return publication.ErrStalePublication
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='published',public_grant_id=NULL,retiring_asset_id=NULL,retiring_grant_id=NULL,published_at=$4,updated_at=$4 WHERE issue_id=$1 AND series=$2 AND locale=$3`, payload.IssueID, payload.Series, payload.Locale, now); err != nil {
		return err
	}
	if err := recalculateBulletinIssueStatus(ctx, tx, payload.IssueID, issueVersion, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(issue_id,series,locale) DO UPDATE SET title=EXCLUDED.title,subtitle=EXCLUDED.subtitle,asset_id=EXCLUDED.asset_id,published_at=EXCLUDED.published_at,version=EXCLUDED.version`, payload.IssueID, payload.Series, payload.Locale, title, subtitle, payload.AssetID, now, issueVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.publication_workflow SET status='completed',updated_at=$2 WHERE id=$1`, payload.WorkflowID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	if err := recordInboxPublication(ctx, tx, r.inboxPublicationsAfter, "bulletin.published", payload.IssueID, payload.Series, payload.Locale, issueVersion, now); err != nil {
		return err
	}
	if payload.NotifySubscribers {
		versions, err := queryVersions(ctx, tx, payload.IssueID)
		if err != nil {
			return err
		}
		notification := buildBulletinNotification(payload.IssueID, payload.Series, issueNumber, payload.ActorID, versions)
		notificationID := platform.NewID()
		key := fmt.Sprintf("notification:v2:bulletin:%s:%s:g0", payload.IssueID, payload.Series)
		notification.RequestID, notification.IdempotencyKey = notificationID, key
		encodedNotification, err := json.Marshal(notification)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
			SELECT $1,'engagement-api','bulletin.notification.submit','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6
			WHERE NOT EXISTS (
				SELECT 1 FROM hhc_web.outbox_event
				WHERE aggregate_type='bulletin' AND aggregate_id=$2 AND event_type='bulletin.notification.submit'
				  AND COALESCE(payload_json#>>'{source,series}','general')=$7
			)
			ON CONFLICT(destination,idempotency_key) DO NOTHING`, notificationID, payload.IssueID, issueVersion, encodedNotification, key, now, payload.Series); err != nil {
			return err
		}
	}
	if retiringAssetID != "" && retiringAssetID != payload.AssetID {
		retirePayload, _ := json.Marshal(publication.UnpublishPayload{
			ActorID: payload.ActorID, RequestID: event.ID,
			IssueID: payload.IssueID, Series: payload.Series, Locale: payload.Locale, AssetID: retiringAssetID,
			GrantID: retiringGrantID, AggregateVersion: issueVersion,
		})
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
			VALUES($1,'asset-api','bulletin.asset.retire','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)
			ON CONFLICT(destination,idempotency_key) DO NOTHING`,
			platform.NewID(), payload.IssueID, issueVersion, retirePayload,
			fmt.Sprintf("bulletin:%s:%s:%s:retire:%s", payload.IssueID, payload.Series, payload.Locale, retiringAssetID), now); err != nil {
			return err
		}
	}
	if r.llmBulletinExtraction && payload.Series == "general" && payload.Locale == "zh-Hant" {
		if err := enqueueOnlineBulletinWake(ctx, tx, event.ID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func buildBulletinNotification(issueID, series string, issueNumber sql.NullInt64, actor string, versions []bulletins.Version) publication.NotificationSubmissionPayload {
	issue := bulletins.Issue{ID: issueID, Versions: versions}
	if issueNumber.Valid {
		number := int(issueNumber.Int64)
		issue.IssueNumber = &number
	}
	preview, _ := bulletins.BuildNotification(issue, series)
	return notificationPayloadFromPreview(issueID, actor, 0, preview)
}

const englishBulletinNotificationFallback = "The latest weekly bulletin is now available."

func notificationPayloadFromPreview(issueID, actor string, generation int, preview bulletins.NotificationPreview) publication.NotificationSubmissionPayload {
	notifications := make([]publication.NotificationCommand, len(preview.Notifications))
	for index, command := range preview.Notifications {
		translations := make(map[string]publication.NotificationTranslation, len(command.Translations))
		for locale, translation := range command.Translations {
			translations[locale] = publication.NotificationTranslation(translation)
		}
		notifications[index] = publication.NotificationCommand{Channel: command.Channel, AudienceType: command.AudienceType, Translations: translations, RequiredEntitlements: command.RequiredEntitlements}
	}
	return publication.NotificationSubmissionPayload{
		Name: preview.Name, Source: &publication.BulletinSource{Kind: "bulletin", IssueID: issueID, Series: preview.Series, NotificationGeneration: generation}, ActorID: actor,
		Notifications: notifications,
	}
}

func (r *Repository) BulletinNotificationState(ctx context.Context, issueID, series string) (*int, *time.Time, error) {
	var generation int
	var createdAt time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT (payload_json#>>'{source,notificationGeneration}')::int,created_at
		FROM hhc_web.outbox_event
		WHERE aggregate_type='bulletin' AND aggregate_id=$1 AND event_type='bulletin.notification.submit'
		  AND COALESCE(payload_json#>>'{source,series}','general')=$2
		ORDER BY (payload_json#>>'{source,notificationGeneration}')::int DESC,created_at DESC
		LIMIT 1`, issueID, series).Scan(&generation, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &generation, &createdAt, nil
}

func (r *Repository) SubmitBulletinNotification(ctx context.Context, issueID, series, actor, clientKey string, now time.Time) (bulletins.NotificationSubmissionResult, error) {
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	defer tx.Rollback()
	var issue bulletins.Issue
	var issueNumber sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT id::text,issue_number FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, issueID).Scan(&issue.ID, &issueNumber); errors.Is(err, sql.ErrNoRows) {
		return bulletins.NotificationSubmissionResult{}, bulletins.ErrNotFound
	} else if err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	manualKey := "manual-bulletin:" + clientKey
	var existingGeneration int
	err = tx.QueryRowContext(ctx, `SELECT (payload_json#>>'{source,notificationGeneration}')::int FROM hhc_web.outbox_event WHERE destination='engagement-api' AND idempotency_key=$1`, manualKey).Scan(&existingGeneration)
	if err == nil {
		if err = tx.Commit(); err != nil {
			return bulletins.NotificationSubmissionResult{}, err
		}
		return bulletins.NotificationSubmissionResult{IssueID: issueID, Series: series, NotificationGeneration: existingGeneration}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return bulletins.NotificationSubmissionResult{}, err
	}
	issue.Versions, err = queryVersions(ctx, tx, issueID)
	if err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	if issueNumber.Valid {
		number := int(issueNumber.Int64)
		issue.IssueNumber = &number
	}
	preview, err := bulletins.BuildNotification(issue, series)
	if err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	var latest sql.NullInt64
	if err = tx.QueryRowContext(ctx, `
		SELECT max((payload_json#>>'{source,notificationGeneration}')::int)
		FROM hhc_web.outbox_event
		WHERE aggregate_type='bulletin' AND aggregate_id=$1 AND event_type='bulletin.notification.submit'
		  AND COALESCE(payload_json#>>'{source,series}','general')=$2`, issueID, series).Scan(&latest); err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	generation := 0
	if latest.Valid {
		generation = int(latest.Int64) + 1
	}
	payload := notificationPayloadFromPreview(issueID, actor, generation, preview)
	eventID := platform.NewID()
	payload.RequestID = eventID
	payload.IdempotencyKey = fmt.Sprintf("notification:v2:bulletin:%s:%s:g%d", issueID, series, generation)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
		VALUES($1,'engagement-api','bulletin.notification.submit','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)`, eventID, issueID, int64(generation), encoded, manualKey, now); err != nil {
		return bulletins.NotificationSubmissionResult{}, mapConflict(err)
	}
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.notification.submit", issueID, actor, now); err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return bulletins.NotificationSubmissionResult{}, err
	}
	return bulletins.NotificationSubmissionResult{IssueID: issueID, Series: series, NotificationGeneration: generation}, nil
}

func (r *Repository) CompleteUnpublish(ctx context.Context, event publication.Event, now time.Time) error {
	var payload publication.UnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	edition, ok := bulletins.NormalizeBulletinEdition(payload.Series, payload.Locale)
	if !ok {
		return bulletins.ErrInvalid
	}
	payload.Series = string(edition.Series)
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
		SET status='unpublished',public_grant_id=NULL,retiring_asset_id=NULL,retiring_grant_id=NULL,updated_at=$5
		WHERE issue_id=$1 AND series=$2 AND locale=$3 AND status='unpublishing'
		  AND EXISTS(SELECT 1 FROM hhc_web.bulletin_issue WHERE id=$1 AND version=$4)`,
		payload.IssueID, payload.Series, payload.Locale, payload.AggregateVersion, now)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows == 0 {
		if err != nil {
			return err
		}
		return publication.ErrStalePublication
	}
	if err := recalculateBulletinIssueStatus(ctx, tx, payload.IssueID, payload.AggregateVersion, now); err != nil {
		return err
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

func recalculateBulletinIssueStatus(ctx context.Context, tx *sql.Tx, issueID string, expectedVersion int64, now time.Time) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE hhc_web.bulletin_issue
		SET status=CASE
				WHEN EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND status='publishing') THEN 'publishing'
				WHEN EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND status='unpublishing') THEN 'unpublishing'
				WHEN EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND status='published') THEN 'published'
				WHEN EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND status='unpublished') THEN 'unpublished'
				ELSE 'draft'
		END,
		published_at=CASE WHEN EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND status='published') THEN COALESCE(published_at,$3) ELSE published_at END,
		updated_at=$3
		WHERE id=$1 AND version=$2`, issueID, expectedVersion, now)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows == 0 {
		if err != nil {
			return err
		}
		return publication.ErrStalePublication
	}
	return nil
}

func (r *Repository) Complete(ctx context.Context, id string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, id, now)
	return err
}

func (r *Repository) CompleteNotification(ctx context.Context, event publication.Event, now time.Time) error {
	var payload publication.BulletinNotificationPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1 AND status='processing'`, event.ID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET notification_status='queued',notification_queued_at=$2,notification_error_code='',updated_at=$2 WHERE id=$1 AND notification_status='pending'`, payload.IssueID, now); err != nil {
		return err
	}
	return tx.Commit()
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
