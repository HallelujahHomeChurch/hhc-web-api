package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

const protectedBulletinColumns = `i.id::text,i.issue_number,i.issue_date::text,p.series,p.locale,p.title,p.subtitle,p.asset_id,p.published_at,p.version`

func (r *Repository) BulletinEntitlement(ctx context.Context, series, locale string) (string, error) {
	var code string
	err := r.db.QueryRowContext(ctx, `SELECT entitlement_code FROM hhc_web.bulletin_entitlement_policy WHERE series=$1 AND locale=$2`, series, locale).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", bulletins.ErrNotFound
	}
	return code, err
}

func (r *Repository) ListProtected(ctx context.Context, series, locale string, page, size int, issueNumber *int) (bulletins.ProtectedPage, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_published_version p ON p.issue_id=i.id WHERE p.series=$1 AND p.locale=$2 AND ($3::integer IS NULL OR i.issue_number=$3)`, series, locale, issueNumber).Scan(&total); err != nil {
		return bulletins.ProtectedPage{}, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+protectedBulletinColumns+` FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_published_version p ON p.issue_id=i.id WHERE p.series=$1 AND p.locale=$2 AND ($3::integer IS NULL OR i.issue_number=$3) ORDER BY i.issue_number DESC NULLS LAST,i.issue_date DESC LIMIT $4 OFFSET $5`, series, locale, issueNumber, size, (page-1)*size)
	if err != nil {
		return bulletins.ProtectedPage{}, err
	}
	defer rows.Close()
	items := make([]bulletins.ProtectedBulletin, 0)
	for rows.Next() {
		item, err := scanProtectedBulletin(rows)
		if err != nil {
			return bulletins.ProtectedPage{}, err
		}
		items = append(items, item)
	}
	return bulletins.ProtectedPage{Items: items, Page: page, PageSize: size, Total: total}, rows.Err()
}

func (r *Repository) GetProtectedLatest(ctx context.Context, series, locale string) (bulletins.ProtectedBulletin, error) {
	return scanProtectedBulletin(r.db.QueryRowContext(ctx, `SELECT `+protectedBulletinColumns+` FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_published_version p ON p.issue_id=i.id WHERE p.series=$1 AND p.locale=$2 ORDER BY i.issue_number DESC NULLS LAST,i.issue_date DESC LIMIT 1`, series, locale))
}

func (r *Repository) GetProtectedVersion(ctx context.Context, issueID, series, locale string) (bulletins.ProtectedBulletin, error) {
	return scanProtectedBulletin(r.db.QueryRowContext(ctx, `SELECT `+protectedBulletinColumns+` FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_published_version p ON p.issue_id=i.id WHERE i.id=$1 AND p.series=$2 AND p.locale=$3`, issueID, series, locale))
}

type protectedScanner interface{ Scan(...any) error }

func scanProtectedBulletin(row protectedScanner) (bulletins.ProtectedBulletin, error) {
	var item bulletins.ProtectedBulletin
	err := row.Scan(&item.IssueID, &item.IssueNumber, &item.IssueDate, &item.Series, &item.Locale, &item.Title, &item.Subtitle, &item.ProtectedAssetID, &item.PublishedAt, &item.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return item, bulletins.ErrNotFound
	}
	if err != nil {
		return item, err
	}
	if item.ProtectedAssetID == "" {
		return bulletins.ProtectedBulletin{}, fmt.Errorf("published bulletin asset is missing")
	}
	item.DownloadName = item.IssueDate + "-" + item.Title + ".pdf"
	if item.IssueNumber != nil {
		item.DownloadName = fmt.Sprintf("%d-%s.pdf", *item.IssueNumber, item.Title)
	}
	return item, nil
}
