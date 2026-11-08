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
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

func (r *Repository) DiscoverOnlineBulletins(ctx context.Context, series string, locales []string, offset, limit int, issueNumber *int) (onlinebulletins.DiscoveryPage, error) {
	result := onlinebulletins.DiscoveryPage{Items: []onlinebulletins.DiscoveryItem{}, Offset: offset, Limit: limit}
	if len(locales) < 1 || len(locales) > 3 || offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return result, bulletins.ErrInvalid
	}
	for _, locale := range locales {
		if _, ok := bulletins.NormalizeBulletinEdition(series, locale); !ok {
			return result, bulletins.ErrInvalid
		}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	const from = ` FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id
 LEFT JOIN hhc_web.bulletin_published_version p ON p.issue_id=v.issue_id AND p.series=v.series AND p.locale=v.locale
 LEFT JOIN hhc_web.online_bulletin_document d ON d.issue_id=v.issue_id AND d.series=v.series AND d.content_locale=v.locale
 LEFT JOIN hhc_web.online_bulletin_publication o ON o.document_id=d.id AND o.revision=d.published_revision AND i.issue_number>=1739
 WHERE v.series=$1 AND v.locale=ANY($2::text[]) AND (p.issue_id IS NOT NULL OR o.document_id IS NOT NULL) AND ($3::integer IS NULL OR i.issue_number=$3)`
	if err = tx.QueryRowContext(ctx, `SELECT count(DISTINCT i.id)`+from, series, locales, issueNumber).Scan(&result.Total); err != nil {
		return result, err
	}
	const projection = `SELECT i.id::text AS id,i.issue_number,i.issue_date::text AS issue_date,v.locale,COALESCE(o.canonical_metadata,jsonb_build_object('title',p.title,'subtitle',p.subtitle,'issueNumber',COALESCE(i.issue_number,0),'date',i.issue_date::text)) AS metadata,COALESCE(o.document_id::text,'') AS document_id,o.revision,p.issue_id IS NOT NULL AS pdf_published`
	rows, err := tx.QueryContext(ctx, `WITH editions AS (`+projection+from+`), selected AS (SELECT DISTINCT id,issue_number,issue_date FROM editions ORDER BY issue_number DESC NULLS LAST,issue_date DESC,id DESC LIMIT $4 OFFSET $5) SELECT e.id,e.issue_number,e.issue_date,e.locale,e.metadata,e.document_id,e.revision,e.pdf_published FROM editions e JOIN selected s ON s.id=e.id ORDER BY s.issue_number DESC NULLS LAST,s.issue_date DESC,s.id DESC,e.locale`, series, locales, issueNumber, limit, offset)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		item := onlinebulletins.DiscoveryItem{Series: series}
		var metadata []byte
		if err = rows.Scan(&item.IssueID, &item.IssueNumber, &item.IssueDate, &item.ContentLocale, &metadata, &item.DocumentID, &item.OnlineRevision, &item.PDFPublished); err != nil {
			rows.Close()
			return result, err
		}
		if err = json.Unmarshal(metadata, &item.CanonicalMetadata); err != nil {
			rows.Close()
			return result, err
		}
		if item.PDFPublished {
			item.Download = &onlinebulletins.PDFAction{IssueID: item.IssueID, Series: series, Locale: item.ContentLocale}
		}
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (r *Repository) OpenOnlineBulletin(ctx context.Context, issue, series, locale, actor, privacy string, input onlinebulletins.ReaderAccessInput, now time.Time) (onlinebulletins.ReaderResponse, error) {
	var result onlinebulletins.ReaderResponse
	if uuid.Validate(issue) != nil || uuid.Validate(actor) != nil || uuid.Validate(input.ClientRequestID) != nil || input.ReceiptID != "" && uuid.Validate(input.ReceiptID) != nil || input.Revision != nil && (*input.Revision < 1 || *input.Revision > 9007199254740991) || privacy == "" || len(privacy) > 200 || now.IsZero() {
		return result, bulletins.ErrInvalid
	}
	if _, ok := bulletins.NormalizeBulletinEdition(series, locale); !ok {
		return result, bulletins.ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// Reuse the PDF subject fence so erase/restrict cannot race content issuance.
	if err = lockWatermarkSubject(ctx, tx, actor); err != nil {
		return result, err
	}
	var restricted bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1)`, watermarkSubjectHash(actor)).Scan(&restricted); err != nil {
		return result, err
	}
	if restricted {
		return result, bulletins.ErrNotFound
	}
	result.Document.IssueID, result.Document.Series, result.Document.ContentLocale = issue, series, locale
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT d.id::text,d.published_revision,d.metadata_sync_pending,EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version p WHERE p.issue_id=d.issue_id AND p.series=d.series AND p.locale=d.content_locale) FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id WHERE d.issue_id=$1 AND d.series=$2 AND d.content_locale=$3 AND d.published_revision IS NOT NULL AND i.issue_number>=1739 FOR SHARE OF i,d`, issue, series, locale).Scan(&result.Document.DocumentID, &current, &result.Document.MetadataSyncPending, &result.Document.PDFPublished)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	revision := current
	if input.Revision != nil {
		revision = *input.Revision
	}
	requestJSON, _ := json.Marshal(struct {
		Issue, Series, Locale string
		Input                 onlinebulletins.ReaderAccessInput
	}{issue, series, locale, input})
	sum := sha256.Sum256(requestJSON)
	requestHash := hex.EncodeToString(sum[:])
	var previousHash, receipt string
	var validated, until, expires time.Time
	err = tx.QueryRowContext(ctx, `SELECT q.request_hash,q.receipt_id::text,q.validated_at,q.offline_valid_until,r.revision,r.expires_at FROM hhc_web.online_bulletin_reader_request q JOIN hhc_web.online_bulletin_reader_receipt r ON r.id=q.receipt_id WHERE q.account_id=$1 AND q.client_request_id=$2`, actor, input.ClientRequestID).Scan(&previousHash, &receipt, &validated, &until, &revision, &expires)
	replay := err == nil
	if replay && (previousHash != requestHash || !expires.After(now)) {
		return result, bulletins.ErrConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var documentJSON, metadataJSON []byte
	var proof string
	err = tx.QueryRowContext(ctx, `SELECT r.document_json,p.canonical_metadata,COALESCE(r.layout_validation_hash,'') FROM hhc_web.online_bulletin_publication p JOIN hhc_web.online_bulletin_revision r ON r.document_id=p.document_id AND r.revision=p.revision WHERE p.document_id=$1 AND p.revision=$2`, result.Document.DocumentID, revision).Scan(&documentJSON, &metadataJSON, &proof)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrConflict
	}
	if err != nil {
		return result, err
	}
	var document onlinebulletins.Document
	if err = json.Unmarshal(documentJSON, &document); err != nil {
		return result, err
	}
	if err = json.Unmarshal(metadataJSON, &result.Document.CanonicalMetadata); err != nil {
		return result, err
	}
	if _, err = onlinebulletins.ValidateDocument(document); err != nil || len(proof) != 64 || document.LayoutManifest.LayoutValidationHash != proof {
		return result, errors.New("published layout unavailable")
	}
	if result.Document.Content, err = onlinebulletins.ProjectMemberContent(document); err != nil {
		return result, err
	}
	result.Document.Revision = revision
	if result.Document.PDFPublished {
		result.Document.Download = &onlinebulletins.PDFAction{IssueID: issue, Series: series, Locale: locale}
	}
	if !replay {
		receipt = input.ReceiptID
		validated = now
		until = now.Add(onlinebulletins.OfflineValidity)
	}
	code := ""
	if receipt != "" {
		err = tx.QueryRowContext(ctx, `SELECT trace_code FROM hhc_web.online_bulletin_reader_receipt WHERE id=$1 AND account_id=$2 AND document_id=$3 AND revision=$4 AND expires_at>$5`, receipt, actor, result.Document.DocumentID, revision, now).Scan(&code)
		if errors.Is(err, sql.ErrNoRows) {
			return result, bulletins.ErrConflict
		}
		if err != nil {
			return result, err
		}
	} else {
		receipt = uuid.NewString()
		code, err = bulletinwatermark.NewCode()
		if err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_reader_receipt(id,account_id,document_id,revision,trace_code,privacy_notice_version,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, receipt, actor, result.Document.DocumentID, revision, code, privacy, now, now.Add(bulletinwatermark.Retention))
		if err != nil {
			return result, err
		}
	}
	if !replay {
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_reader_request(account_id,client_request_id,receipt_id,request_hash,validated_at,offline_valid_until) VALUES($1,$2,$3,$4,$5,$6)`, actor, input.ClientRequestID, receipt, requestHash, validated, until)
		if err != nil {
			return result, err
		}
	}
	result.Access = onlinebulletins.ReaderAccess{AccountID: actor, DocumentID: result.Document.DocumentID, Series: series, ContentLocale: locale, Revision: revision, CurrentRevision: current, ReceiptID: receipt, TraceCode: bulletinwatermark.FormatCode(code), ValidatedAt: validated, OfflineValidUntil: until}
	if err = tx.Commit(); err != nil {
		return onlinebulletins.ReaderResponse{}, err
	}
	return result, nil
}
