package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func groupChildModule(pageKey string) (content.Module, bool) {
	switch pageKey {
	case "home":
		return content.ModuleVideos, true
	case "about":
		return content.ModuleHistory, true
	default:
		return "", false
	}
}

func collectGroupManifest(ctx context.Context, tx *sql.Tx, page content.Item, targetVersion int64) (content.PageGroupManifest, error) {
	module, ok := groupChildModule(page.PageKey)
	if !ok {
		return content.PageGroupManifest{}, content.ErrInvalid
	}
	children, err := lockGroupChildren(ctx, tx, module)
	if err != nil {
		return content.PageGroupManifest{}, err
	}
	items := make([]content.PageGroupManifestItem, 0, len(children))
	for _, child := range children {
		var action content.GroupAction
		target := child.Version
		switch child.Status {
		case content.StatusPendingRemoval:
			action, target = content.GroupActionRemove, child.Version+1
		case content.StatusPublished:
			action = content.GroupActionKeep
		case content.StatusDraft, content.StatusPublishFailed:
			action, target = content.GroupActionPublish, child.Version+1
		case content.StatusUnpublished:
			continue
		default:
			return content.PageGroupManifest{}, content.ErrConflict
		}
		if action != content.GroupActionRemove {
			if err := requireFiveLocales(child); err != nil {
				return content.PageGroupManifest{}, err
			}
		}
		items = append(items, content.PageGroupManifestItem{ID: child.ID, SourceVersion: child.Version, TargetVersion: target, Action: action})
	}
	return content.NewPageGroupManifest(page.ID, page.Version, targetVersion, module, items)
}

func insertGroupManifest(ctx context.Context, tx *sql.Tx, manifest content.PageGroupManifest, status, actor string, now time.Time) error {
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO hhc_web.page_publication_manifest(page_id,page_version,child_module,manifest_sha256,manifest_json,publication_status,created_by,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, manifest.PageID, manifest.PageTargetVersion, manifest.ChildModule, manifest.SHA256, payload, status, actor, now)
	return err
}

func loadPendingGroupManifest(ctx context.Context, tx *sql.Tx, pageID string, version int64, expectedSHA string) (content.PageGroupManifest, error) {
	var storedSHA string
	var payload []byte
	err := tx.QueryRowContext(ctx, `SELECT manifest_sha256,manifest_json FROM hhc_web.page_publication_manifest WHERE page_id=$1 AND page_version=$2 AND publication_status='pending' FOR UPDATE`, pageID, version).Scan(&storedSHA, &payload)
	if errors.Is(err, sql.ErrNoRows) || expectedSHA == "" || storedSHA != expectedSHA {
		return content.PageGroupManifest{}, publication.ErrStalePublication
	}
	if err != nil {
		return content.PageGroupManifest{}, err
	}
	var manifest content.PageGroupManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return content.PageGroupManifest{}, err
	}
	canonical, err := content.NewPageGroupManifest(manifest.PageID, manifest.PageSourceVersion, manifest.PageTargetVersion, manifest.ChildModule, manifest.Items)
	if err != nil || canonical.SHA256 != storedSHA || manifest.PageID != pageID || manifest.PageTargetVersion != version {
		return content.PageGroupManifest{}, publication.ErrStalePublication
	}
	return manifest, nil
}

func validateGroupManifestChildren(ctx context.Context, tx *sql.Tx, manifest content.PageGroupManifest) (map[string]content.Item, error) {
	children, err := lockGroupChildren(ctx, tx, manifest.ChildModule)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]content.Item, len(children))
	for _, child := range children {
		byID[child.ID] = child
	}
	for _, member := range manifest.Items {
		child, ok := byID[member.ID]
		if !ok || child.Version != member.SourceVersion {
			return nil, publication.ErrStalePublication
		}
		valid := member.Action == content.GroupActionKeep && child.Status == content.StatusPublished ||
			member.Action == content.GroupActionPublish && (child.Status == content.StatusDraft || child.Status == content.StatusPublishFailed) ||
			member.Action == content.GroupActionRemove && child.Status == content.StatusPendingRemoval
		if !valid {
			return nil, publication.ErrStalePublication
		}
	}
	return byID, nil
}

func capturePublishedBaseline(ctx context.Context, tx *sql.Tx, page content.Item, actor string, now time.Time) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.page_publication_manifest WHERE page_id=$1)`, page.ID).Scan(&exists); err != nil || exists {
		return err
	}
	var minimum, maximum sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(version),MAX(version) FROM hhc_web.public_projection WHERE resource_type='pages' AND resource_id=$1`, page.ID).Scan(&minimum, &maximum); err != nil {
		return err
	}
	if !minimum.Valid {
		return nil
	}
	if minimum.Int64 != maximum.Int64 {
		return content.ErrConflict
	}
	module, ok := groupChildModule(page.PageKey)
	if !ok {
		return content.ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT resource_id::text,MIN(version),MAX(version)
		FROM hhc_web.public_projection
		WHERE resource_type=$1
		GROUP BY resource_id
		ORDER BY resource_id`, module)
	if err != nil {
		return err
	}
	var items []content.PageGroupManifestItem
	for rows.Next() {
		var id string
		var first, last int64
		if err := rows.Scan(&id, &first, &last); err != nil {
			rows.Close()
			return err
		}
		if first != last {
			rows.Close()
			return content.ErrConflict
		}
		items = append(items, content.PageGroupManifestItem{ID: id, SourceVersion: first, TargetVersion: first, Action: content.GroupActionKeep})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	manifest, err := content.NewPageGroupManifest(page.ID, minimum.Int64, minimum.Int64, module, items)
	if err != nil {
		return err
	}
	return insertGroupManifest(ctx, tx, manifest, "published", actor, now)
}

func requireFiveLocales(item content.Item) error {
	if !content.IsPublishable(item) || len(item.Translations) != 5 {
		return content.ErrNotPublishable
	}
	want := []string{"en", "ja", "ko", "zh-Hans", "zh-Hant"}
	got := make([]string, len(item.Translations))
	for index := range item.Translations {
		got[index] = item.Translations[index].Locale
	}
	sort.Strings(got)
	for index := range want {
		if got[index] != want[index] {
			return content.ErrNotPublishable
		}
	}
	return nil
}

func writeGroupProjection(ctx context.Context, tx *sql.Tx, item content.Item, translation content.Translation, now time.Time) error {
	var projection any
	var key, route string
	if item.Module == content.ModulePages {
		locales := make([]string, 0, len(item.Translations))
		for _, candidate := range item.Translations {
			locales = append(locales, candidate.Locale)
		}
		sortPublicLocales(locales)
		projection = content.PublicEditorialPage{PageKey: item.PageKey, Template: item.PageTemplate, RoutePath: item.RoutePath, Indexable: item.Indexable, Content: translation.BodyJSON, ResolvedLocale: translation.Locale, AvailableLocales: locales, Version: item.Version, PublishedAt: *item.PublishedAt}
		key, route = fmt.Sprintf("page:%s:%s", translation.Locale, item.PageKey), item.RoutePath
	} else {
		value := publicContent(item, translation)
		projection, key, route = value, fmt.Sprintf("%s:%s:%s", item.Module, translation.Locale, item.ID), value.Href
	}
	payload, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	etag := fmt.Sprintf("%x", sha256.Sum256(payload))
	_, err = tx.ExecContext(ctx, `
		INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(projection_key) DO UPDATE SET route_path=EXCLUDED.route_path,version=EXCLUDED.version,etag=EXCLUDED.etag,payload_json=EXCLUDED.payload_json,updated_at=EXCLUDED.updated_at`, key, item.Module, item.ID, translation.Locale, route, item.Version, etag, payload, now)
	return err
}

func (r *Repository) publishPageGroup(ctx context.Context, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, content.ModulePages, id, expected); err != nil {
		return content.Item{}, err
	}
	page, err := loadContent(ctx, tx, content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	module, grouped := groupChildModule(page.PageKey)
	if !grouped || (page.PageKey == "home" && page.PageTemplate != "home.v1") || requireFiveLocales(page) != nil {
		return content.Item{}, content.ErrNotPublishable
	}
	if err := capturePublishedBaseline(ctx, tx, page, actor, now); err != nil {
		return content.Item{}, err
	}
	manifest, err := collectGroupManifest(ctx, tx, page, expected+1)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertGroupManifest(ctx, tx, manifest, "published", actor, now); err != nil {
		return content.Item{}, err
	}
	children, err := loadGroupChildren(ctx, tx, module)
	if err != nil {
		return content.Item{}, err
	}
	byID := make(map[string]content.Item, len(children))
	for _, child := range children {
		byID[child.ID] = child
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE (resource_type='pages' AND resource_id=$1) OR resource_type=$2`, id, module); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',version=$2,first_published_at=COALESCE(first_published_at,$3),published_at=$3,updated_by=$4,updated_at=$3 WHERE id=$1`, id, manifest.PageTargetVersion, now, actor); err != nil {
		return content.Item{}, err
	}
	page, err = loadContent(ctx, tx, content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	for _, translation := range page.Translations {
		if err := writeGroupProjection(ctx, tx, page, translation, now); err != nil {
			return content.Item{}, err
		}
	}
	for _, member := range manifest.Items {
		child := byID[member.ID]
		switch member.Action {
		case content.GroupActionKeep:
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',first_published_at=COALESCE(first_published_at,$2),published_at=$2,updated_at=$2 WHERE id=$1 AND version=$3`, child.ID, now, member.SourceVersion); err != nil {
				return content.Item{}, err
			}
		case content.GroupActionPublish:
			if err := updateGroupChildStatus(ctx, tx, child.ID, member.SourceVersion, member.TargetVersion, content.StatusPublished, actor, now); err != nil {
				return content.Item{}, err
			}
		case content.GroupActionRemove:
			if err := updateGroupChildStatus(ctx, tx, child.ID, member.SourceVersion, member.TargetVersion, content.StatusUnpublished, actor, now); err != nil {
				return content.Item{}, err
			}
			continue
		}
		child, err = loadContent(ctx, tx, module, child.ID)
		if err != nil {
			return content.Item{}, err
		}
		for _, translation := range child.Translations {
			if err := writeGroupProjection(ctx, tx, child, translation, now); err != nil {
				return content.Item{}, err
			}
		}
		if member.Action == content.GroupActionPublish {
			if err := insertRevision(ctx, tx, child, actor, now); err != nil {
				return content.Item{}, err
			}
		}
	}
	if page.PageKey == "home" && page.BannerPublicGrantID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_item SET published_banner_asset_id=NULL,banner_public_grant_id=NULL,published_banner_version=NULL WHERE content_id=$1`, id); err != nil {
			return content.Item{}, err
		}
		if err := enqueueGrantRevoke(ctx, tx, "home", id, manifest.PageTargetVersion, publication.PublishedAsset{Usage: "banner", AssetID: page.PublishedBannerAssetID, GrantID: page.BannerPublicGrantID}, now); err != nil {
			return content.Item{}, err
		}
		page, err = loadContent(ctx, tx, content.ModulePages, id)
		if err != nil {
			return content.Item{}, err
		}
	}
	if err := insertRevision(ctx, tx, page, actor, now); err != nil {
		return content.Item{}, err
	}
	return page, tx.Commit()
}

func (r *Repository) unpublishAboutGroup(ctx context.Context, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, content.ModulePages, id, expected); err != nil {
		return content.Item{}, err
	}
	page, err := loadContent(ctx, tx, content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	if page.PageKey != "about" || !page.IsPublished {
		return content.Item{}, content.ErrNotPublishable
	}
	if err := capturePublishedBaseline(ctx, tx, page, actor, now); err != nil {
		return content.Item{}, err
	}
	children, err := lockGroupChildren(ctx, tx, content.ModuleHistory)
	if err != nil {
		return content.Item{}, err
	}
	items := make([]content.PageGroupManifestItem, 0, len(children))
	for _, child := range children {
		if child.IsPublished {
			items = append(items, content.PageGroupManifestItem{ID: child.ID, SourceVersion: child.Version, TargetVersion: child.Version + 1, Action: content.GroupActionRemove})
		}
	}
	manifest, err := content.NewPageGroupManifest(page.ID, page.Version, page.Version+1, content.ModuleHistory, items)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertGroupManifest(ctx, tx, manifest, "unpublished", actor, now); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE (resource_type='pages' AND resource_id=$1) OR resource_type='history'`, id); err != nil {
		return content.Item{}, err
	}
	for _, member := range manifest.Items {
		if err := updateGroupChildStatus(ctx, tx, member.ID, member.SourceVersion, member.TargetVersion, content.StatusUnpublished, actor, now); err != nil {
			return content.Item{}, err
		}
		child, err := loadContent(ctx, tx, content.ModuleHistory, member.ID)
		if err != nil {
			return content.Item{}, err
		}
		if err := insertRevision(ctx, tx, child, actor, now); err != nil {
			return content.Item{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='unpublished',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, manifest.PageTargetVersion, actor, now); err != nil {
		return content.Item{}, err
	}
	page, err = loadContent(ctx, tx, content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, page, actor, now); err != nil {
		return content.Item{}, err
	}
	return page, tx.Commit()
}

func (r *Repository) unpublishHomeGroup(ctx context.Context, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, content.ModulePages, id, expected); err != nil {
		return content.Item{}, err
	}
	page, err := loadContent(ctx, tx, content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	if page.PageKey != "home" || !page.IsPublished {
		return content.Item{}, content.ErrNotPublishable
	}
	if err := capturePublishedBaseline(ctx, tx, page, actor, now); err != nil {
		return content.Item{}, err
	}
	children, err := lockGroupChildren(ctx, tx, content.ModuleVideos)
	if err != nil {
		return content.Item{}, err
	}
	items := make([]content.PageGroupManifestItem, 0, len(children))
	for _, child := range children {
		if child.IsPublished {
			items = append(items, content.PageGroupManifestItem{ID: child.ID, SourceVersion: child.Version, TargetVersion: child.Version + 1, Action: content.GroupActionRemove})
		}
	}
	manifest, err := content.NewPageGroupManifest(page.ID, page.Version, page.Version+1, content.ModuleVideos, items)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertGroupManifest(ctx, tx, manifest, "unpublished", actor, now); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE (resource_type='pages' AND resource_id=$1) OR resource_type='videos'`, id); err != nil {
		return content.Item{}, err
	}
	for _, member := range manifest.Items {
		if err := updateGroupChildStatus(ctx, tx, member.ID, member.SourceVersion, member.TargetVersion, content.StatusUnpublished, actor, now); err != nil {
			return content.Item{}, err
		}
		child, err := loadContent(ctx, tx, content.ModuleVideos, member.ID)
		if err != nil {
			return content.Item{}, err
		}
		if err := insertRevision(ctx, tx, child, actor, now); err != nil {
			return content.Item{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='unpublished',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, manifest.PageTargetVersion, actor, now); err != nil {
		return content.Item{}, err
	}
	if page.BannerPublicGrantID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_item SET published_banner_asset_id=NULL,banner_public_grant_id=NULL,published_banner_version=NULL WHERE content_id=$1`, id); err != nil {
			return content.Item{}, err
		}
		if err := enqueueGrantRevoke(ctx, tx, "home", id, manifest.PageTargetVersion, publication.PublishedAsset{Usage: "banner", AssetID: page.PublishedBannerAssetID, GrantID: page.BannerPublicGrantID}, now); err != nil {
			return content.Item{}, err
		}
	}
	page, err = loadContent(ctx, tx, content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, page, actor, now); err != nil {
		return content.Item{}, err
	}
	return page, tx.Commit()
}

func (r *Repository) RestorePageGroup(ctx context.Context, pageID string, revision, expected int64, actor string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, content.ModulePages, pageID, expected); err != nil {
		return content.Item{}, err
	}
	currentPage, err := loadContent(ctx, tx, content.ModulePages, pageID)
	if err != nil {
		return content.Item{}, err
	}
	module, ok := groupChildModule(currentPage.PageKey)
	if !ok {
		return content.Item{}, content.ErrMethodNotAllowed
	}
	manifest, pageSnapshot, err := loadGroupRevision(ctx, tx, pageID, revision)
	if err != nil {
		return content.Item{}, err
	}
	if manifest.ChildModule != module {
		return content.Item{}, content.ErrInvalid
	}
	children, err := lockGroupChildren(ctx, tx, module)
	if err != nil {
		return content.Item{}, err
	}
	currentByID := make(map[string]content.Item, len(children))
	for _, child := range children {
		currentByID[child.ID] = child
	}
	targetIDs := make(map[string]bool, len(manifest.Items))
	for _, member := range manifest.Items {
		targetIDs[member.ID] = true
		child, exists := currentByID[member.ID]
		if !exists {
			return content.Item{}, content.ErrNotFound
		}
		var payload []byte
		if err := tx.QueryRowContext(ctx, `SELECT snapshot_json FROM hhc_web.content_revision WHERE entry_id=$1 AND version=$2`, member.ID, member.TargetVersion).Scan(&payload); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return content.Item{}, content.ErrNotFound
			}
			return content.Item{}, err
		}
		var snapshot content.Item
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			return content.Item{}, err
		}
		if err := writeTypedContent(ctx, tx, module, child.ID, writeInputFromItem(snapshot), false); err != nil {
			return content.Item{}, err
		}
		if err := replaceTranslations(ctx, tx, child.ID, snapshot.Translations); err != nil {
			return content.Item{}, err
		}
		status := content.StatusDraft
		if member.Action == content.GroupActionRemove {
			status = content.StatusPendingRemoval
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status=$2,version=version+1,updated_by=$3,updated_at=$4 WHERE id=$1`, child.ID, status, actor, now); err != nil {
			return content.Item{}, err
		}
		child, err = loadContent(ctx, tx, module, child.ID)
		if err != nil {
			return content.Item{}, err
		}
		if err := insertRevision(ctx, tx, child, actor, now); err != nil {
			return content.Item{}, err
		}
	}
	for _, child := range children {
		if targetIDs[child.ID] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='pending_removal',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, child.ID, actor, now); err != nil {
			return content.Item{}, err
		}
		child, err = loadContent(ctx, tx, module, child.ID)
		if err != nil {
			return content.Item{}, err
		}
		if err := insertRevision(ctx, tx, child, actor, now); err != nil {
			return content.Item{}, err
		}
	}
	if err := writeRestoredPage(ctx, tx, pageID, writeInputFromItem(pageSnapshot)); err != nil {
		return content.Item{}, err
	}
	if err := replaceTranslations(ctx, tx, pageID, pageSnapshot.Translations); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='draft',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, pageID, actor, now); err != nil {
		return content.Item{}, err
	}
	page, err := loadContent(ctx, tx, content.ModulePages, pageID)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, page, actor, now); err != nil {
		return content.Item{}, err
	}
	return page, tx.Commit()
}

func (r *Repository) pageGroupRevisions(ctx context.Context, pageID string) ([]content.Revision, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.version,r.snapshot_json,r.created_by,r.created_at,m.manifest_json
		FROM hhc_web.content_revision r
		JOIN hhc_web.page_publication_manifest m ON m.page_id=r.entry_id AND m.page_version=r.version
		WHERE r.entry_id=$1 AND m.publication_status IN ('published','unpublished')
		ORDER BY r.version DESC`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []content.Revision
	for rows.Next() {
		var value content.Revision
		var snapshot, manifest []byte
		if err := rows.Scan(&value.Version, &snapshot, &value.CreatedBy, &value.CreatedAt, &manifest); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(snapshot, &value.Snapshot); err != nil {
			return nil, err
		}
		value.GroupManifest = &content.PageGroupManifest{}
		if err := json.Unmarshal(manifest, value.GroupManifest); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadGroupChildren(ctx context.Context, tx *sql.Tx, module content.Module) ([]content.Item, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id::text FROM hhc_web.content_entry WHERE module=$1 ORDER BY id`, module)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	values := make([]content.Item, 0, len(ids))
	for _, id := range ids {
		item, err := loadContent(ctx, tx, module, id)
		if err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, nil
}

func lockGroupChildren(ctx context.Context, tx *sql.Tx, module content.Module) ([]content.Item, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id::text FROM hhc_web.content_entry WHERE module=$1 ORDER BY id FOR UPDATE`, module)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	values := make([]content.Item, 0, len(ids))
	for _, id := range ids {
		item, err := loadContent(ctx, tx, module, id)
		if err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, nil
}

func updateGroupChildStatus(ctx context.Context, tx *sql.Tx, id string, source, target int64, status, actor string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status=$4,version=$3,first_published_at=CASE WHEN $4='published' THEN COALESCE(first_published_at,$5) ELSE first_published_at END,published_at=CASE WHEN $4='published' THEN $5 ELSE published_at END,updated_by=$6,updated_at=$5 WHERE id=$1 AND version=$2`, id, source, target, status, now, actor)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return content.ErrPrecondition
	}
	return nil
}

func loadGroupRevision(ctx context.Context, tx *sql.Tx, pageID string, revision int64) (content.PageGroupManifest, content.Item, error) {
	var manifestPayload, pagePayload []byte
	err := tx.QueryRowContext(ctx, `
		SELECT m.manifest_json,r.snapshot_json
		FROM hhc_web.page_publication_manifest m
		JOIN hhc_web.content_revision r ON r.entry_id=m.page_id AND r.version=m.page_version
		WHERE m.page_id=$1 AND m.page_version=$2 AND m.publication_status IN ('published','unpublished')`, pageID, revision).Scan(&manifestPayload, &pagePayload)
	if errors.Is(err, sql.ErrNoRows) {
		return content.PageGroupManifest{}, content.Item{}, content.ErrNotFound
	}
	if err != nil {
		return content.PageGroupManifest{}, content.Item{}, err
	}
	var manifest content.PageGroupManifest
	var page content.Item
	if err := json.Unmarshal(manifestPayload, &manifest); err != nil {
		return content.PageGroupManifest{}, content.Item{}, err
	}
	if err := json.Unmarshal(pagePayload, &page); err != nil {
		return content.PageGroupManifest{}, content.Item{}, err
	}
	canonical, err := content.NewPageGroupManifest(manifest.PageID, manifest.PageSourceVersion, manifest.PageTargetVersion, manifest.ChildModule, manifest.Items)
	if err != nil || canonical.SHA256 != manifest.SHA256 {
		return content.PageGroupManifest{}, content.Item{}, content.ErrInvalid
	}
	return manifest, page, nil
}

func writeInputFromItem(item content.Item) content.WriteInput {
	return content.WriteInput{AuthorName: item.AuthorName, Slug: item.Slug, DisplayDate: item.DisplayDate, EventDate: item.EventDate, YouTubeVideoID: item.YouTubeVideoID, CoverAssetID: item.CoverAssetID, HomeCoverAssetID: item.HomeCoverAssetID, DetailLayout: item.DetailLayout, Featured: item.Featured, HomeEligible: item.HomeEligible, LocationKey: item.LocationKey, MapHref: item.MapHref, SortOrder: item.SortOrder, PageKey: item.PageKey, PageTemplate: item.PageTemplate, RoutePath: item.RoutePath, Indexable: item.Indexable, BannerAssetID: item.BannerAssetID, Links: item.Links, Locations: item.Locations, Translations: item.Translations}
}
