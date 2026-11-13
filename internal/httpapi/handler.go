package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/logging"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
	"github.com/google/uuid"
)

const maxAdminProxyBody = 1 << 20

const maxBulletinPDFSize = 20 << 20

type assetUploads interface {
	CreateBulletinUpload(context.Context, string, string, string, string, int64, string) (assetclient.CreatedUpload, error)
	CreateNewsCoverUpload(context.Context, string, string, string, string, int64, string) (assetclient.CreatedUpload, error)
	CreateHomeBannerUpload(context.Context, string, string, string, int64, string) (assetclient.CreatedUpload, error)
	CompleteUpload(context.Context, string, assetclient.CompleteUploadInput) (assetclient.Asset, error)
	CreateServiceGrant(context.Context, string, string) (assetclient.Grant, error)
	Get(context.Context, string) (assetclient.Asset, error)
	RequeueScan(context.Context, string) error
}

type engagementProxy interface {
	Forward(context.Context, string, string, io.Reader, string, string) (*http.Response, error)
}

type assetDownloads interface {
	AuthorizedDownload(context.Context, string, string, string, string) (*http.Response, error)
}

type auditOutbox interface {
	Enqueue(context.Context, auditclient.Event, time.Time) (publication.Event, error)
}

type Handler struct {
	gatewayLocationsToken   string
	privateServiceTokens    map[string]string
	gatewayPrivateToken     string
	gatewayLivePrivateToken string
	daprDisabled            bool
	commentAuthors          commentAuthorCache
	memberLegalEnforcement  bool
	legalAcceptance         legalAcceptanceChecker
	legal                   *legal.Service
	memberQualification     memberQualifier
	watermarkRepo           watermarkRepository
	watermarkIdentities     investigationIdentityResolver
	watermarkPrivacyVersion string
	bulletinEntitlements    interface {
		CheckEntitlements(context.Context, string, []string) ([]operationsclient.EntitlementDecision, error)
	}
	service        *bulletins.Service
	content        *content.Service
	siteSettings   *sitesettings.Service
	recordings     *recordings.Service
	db             *sql.DB
	uploads        assetUploads
	downloads      assetDownloads
	engagement     engagementProxy
	translation    TranslationPreviewer
	translationNow func() time.Time
	translationTTL time.Duration
	trustedCaller  string
	daprAPIToken   string
	allowDevCaller bool
	auditOutbox    auditOutbox
	auditNow       func() time.Time
}

func (h *Handler) WithBulletinEntitlements(checker interface {
	CheckEntitlements(context.Context, string, []string) ([]operationsclient.EntitlementDecision, error)
}) *Handler {
	h.bulletinEntitlements = checker
	return h
}

func New(service *bulletins.Service, db *sql.DB, uploads assetUploads) *Handler {
	return NewWithContent(service, nil, db, uploads, "api-gateway", "", false)
}
func NewWithContent(service *bulletins.Service, contentService *content.Service, db *sql.DB, uploads assetUploads, trustedCaller, daprAPIToken string, allowDevCaller bool, engagement ...engagementProxy) *Handler {
	handler := &Handler{service: service, content: contentService, db: db, uploads: uploads, trustedCaller: trustedCaller, daprAPIToken: daprAPIToken, allowDevCaller: allowDevCaller, translationNow: time.Now, translationTTL: 50 * time.Second}
	if len(engagement) > 0 {
		handler.engagement = engagement[0]
	}
	return handler
}
func (h *Handler) WithSiteSettings(service *sitesettings.Service) *Handler {
	h.siteSettings = service
	return h
}

func (h *Handler) WithRecordings(service *recordings.Service) *Handler {
	h.recordings = service
	return h
}
func (h *Handler) WithAssetDownloads(downloads assetDownloads) *Handler {
	h.downloads = downloads
	return h
}
func (h *Handler) WithAuditOutbox(outbox auditOutbox, now func() time.Time) *Handler {
	h.auditOutbox, h.auditNow = outbox, now
	return h
}
func NewWithTranslation(service *bulletins.Service, contentService *content.Service, db *sql.DB, uploads assetUploads, trustedCaller, daprAPIToken string, allowDevCaller bool, previewer TranslationPreviewer, writeDeadline time.Duration, now func() time.Time, engagement ...engagementProxy) *Handler {
	handler := NewWithContent(service, contentService, db, uploads, trustedCaller, daprAPIToken, allowDevCaller, engagement...)
	handler.translation = previewer
	handler.translationTTL = writeDeadline
	handler.translationNow = now
	return handler
}
func (h *Handler) Routes() http.Handler {
	return h.RoutesWithMiddleware(nil)
}

// RoutesWithMiddleware applies server instrumentation after private alias authentication and header sanitization.
func (h *Handler) RoutesWithMiddleware(wrap func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	public := http.NewServeMux()
	public.HandleFunc("GET /api/video-previews/{publicId}", h.recordingShare)
	public.HandleFunc("GET /api/video-previews/{publicId}/cover", h.recordingShare)
	public.HandleFunc("GET /api/recording-previews/{id}", h.recordingShare)
	public.HandleFunc("GET /api/recording-previews/{id}/cover", h.recordingShare)
	mux.Handle("/api/", publicReadDeadline(public))
	live := func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"status": "healthy"}, nil)
	}
	mux.HandleFunc("GET /health", live)
	mux.HandleFunc("GET /health/live", live)
	mux.HandleFunc("GET /ready", h.ready)
	mux.HandleFunc("GET /health/ready", h.ready)
	admin := http.NewServeMux()
	admin.HandleFunc("GET /api/admin/bulletins/template-settings", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminBulletinTemplateSettings)))
	admin.HandleFunc("PUT /api/admin/bulletins/template-settings", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminSaveBulletinTemplateSettings)))
	admin.HandleFunc("GET /api/admin/bulletins/template-settings/revisions", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminBulletinTemplateSettingsHistory)))
	admin.HandleFunc("GET /api/admin/bulletin-learning-rules", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminBulletinLearningRules)))
	admin.HandleFunc("GET /api/admin/bulletin-learning-rules/{signature}", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminBulletinLearningRule)))
	admin.HandleFunc("PUT /api/admin/bulletin-learning-rules/{signature}", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminSetBulletinLearningRule)))
	admin.HandleFunc("POST /api/admin/recordings/watermark-lookups", requireScope("cms:recordings:investigate", h.lookupRecordingWatermark))
	admin.HandleFunc("GET /api/admin/recordings/retention-policy", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminRecordingRetention)))
	for _, base := range []string{"/api/admin/recordings/live-cover-settings", "/api/admin/recordings/{id}/captures/{captureId}/cover-settings"} {
		admin.HandleFunc("GET "+base, requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminLiveCoverSettings)))
		admin.HandleFunc("PUT "+base, requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminSetLiveCoverSettings)))
		admin.HandleFunc("POST "+base+"/uploads", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminUploadLiveCover)))
		admin.HandleFunc("GET "+base+"/uploads/{uploadId}", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminLiveCoverUpload)))
		admin.HandleFunc("GET "+base+"/uploads/{uploadId}/content", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminLiveCoverContent)))
	}
	admin.HandleFunc("GET /api/admin/recordings/{id}/captures/{captureId}/cover-settings/content", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminLiveCoverContent)))
	admin.HandleFunc("POST /api/admin/recordings/retention-policy/preview", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminPreviewRecordingRetention)))
	admin.HandleFunc("PUT /api/admin/recordings/retention-policy", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminUpdateRecordingRetention)))
	admin.HandleFunc("POST /api/admin/bulletins/watermark-lookups", requireScope("cms:bulletins:investigate", h.lookupBulletinWatermark))
	admin.HandleFunc("GET /api/admin/bulletins/{issueId}/watermark-versions", requireScope("cms:bulletins:investigate", h.listBulletinWatermarkVersions))
	admin.HandleFunc("GET /api/admin/bulletins/{issueId}/watermark-investigations", requireScope("cms:bulletins:investigate", h.listBulletinWatermarkInvestigations))
	admin.HandleFunc("GET /api/admin/bulletin-watermark-investigations", requireScope("cms:bulletins:investigate", h.listGlobalBulletinWatermarkInvestigations))
	admin.HandleFunc("POST /api/admin/bulletin-watermark-investigations", requireScope("cms:bulletins:investigate", h.createBulletinWatermarkInvestigation))
	admin.HandleFunc("GET /api/admin/bulletin-watermark-investigations/{id}", requireScope("cms:bulletins:investigate", h.getBulletinWatermarkInvestigation))
	admin.HandleFunc("GET /api/admin/bulletins", requireAnyScope([]string{"cms:bulletins:read", "cms:bulletins:investigate"}, h.adminAuditProvenance(h.adminList)))
	admin.HandleFunc("POST /api/admin/bulletins", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminCreate)))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminGet)))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/source-parts", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminOnlineSourceParts)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/source-repairs", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminOnlineSourceRepair)))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/source-pdf", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminOnlineSourcePDF)))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminOnlineState)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/extractions", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminOnlineExtract)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/conversions", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminOnlineConvert)))
	admin.HandleFunc("PUT /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/draft", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminOnlineSave)))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/comparison", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminOnlineComparison)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/compare", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminOnlineCompare)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/confirm", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminOnlineConfirm)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/revisions/{revision}/restore", requireScope("cms:bulletins:publish", h.adminAuditProvenance(h.adminOnlineRestore)))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/revisions", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminOnlineHistory)))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/revisions/{revision}", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminOnlineRevision)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/recover-publication", requireScope("cms:bulletins:publish", h.adminAuditProvenance(h.adminOnlineRecoverPublication)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/publish", requireScope("cms:bulletins:publish", h.adminAuditProvenance(h.adminOnlinePublish)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/online/{series}/{contentLocale}/unpublish", requireScope("cms:bulletins:publish", h.adminAuditProvenance(h.adminOnlineUnpublish)))
	admin.HandleFunc("PUT /api/admin/bulletins/{issueID}", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminUpdateIssue)))
	admin.HandleFunc("PUT /api/admin/bulletins/{issueID}/versions/{locale}", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminUpdateVersion)))
	admin.HandleFunc("DELETE /api/admin/bulletins/{issueID}/versions/{locale}", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminDeleteVersion)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/upload-sessions", requireScope("cms:bulletins:write", h.adminCreateUpload))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/assets/{assetID}", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminAssetStatus)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/assets/{assetID}/scan/retry", requireScope("cms:bulletins:write", h.adminRetryAssetScan))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/assets/{assetID}/complete", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminCompleteUpload)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/publish", requireScope("cms:bulletins:publish", h.adminAuditProvenance(h.adminPublish)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/unpublish", requireScope("cms:bulletins:publish", h.adminAuditProvenance(h.adminUnpublish)))
	admin.HandleFunc("DELETE /api/admin/bulletins/{issueID}", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminDelete)))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/revisions", requireScope("cms:bulletins:read", h.adminAuditProvenance(h.adminIssueRevisions)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/revisions/{revision}/restore", requireScope("cms:bulletins:write", h.adminAuditProvenance(h.adminRestoreIssueRevision)))
	admin.HandleFunc("POST /api/admin/recordings", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminCreateRecording)))
	admin.HandleFunc("GET /api/admin/recordings", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminListRecordings)))
	admin.HandleFunc("GET /api/admin/recordings/{id}", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminGetRecording)))
	admin.HandleFunc("GET /api/admin/recordings/{id}/covers", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminListRecordingCovers)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/cover-uploads", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminUploadRecordingCover)))
	admin.HandleFunc("PUT /api/admin/recordings/{id}/cover", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminSetRecordingCover)))
	admin.HandleFunc("GET /api/admin/recordings/{id}/covers/{coverId}/content", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminRecordingCoverContent)))
	admin.HandleFunc("PATCH /api/admin/recordings/{id}", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminUpdateRecordingTitle)))
	admin.HandleFunc("DELETE /api/admin/recordings/{id}", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminDeleteRecording)))
	admin.HandleFunc("PATCH /api/admin/recordings/{id}/exposure", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminSetRecordingExposure)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/publish", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminPublishRecording)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/packages", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminCreateRecordingPackage)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/source-uploads", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminCreateRecordingSource)))
	admin.HandleFunc("GET /api/admin/recordings/{id}/source-uploads/{sourceID}", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminRecordingSourceStatus)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/source-uploads/{sourceID}/sign", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminSignRecordingSource)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/source-uploads/{sourceID}/complete", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminCompleteRecordingSource)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/source-uploads/{sourceID}/retry-processing", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminRetryRecordingSource)))
	admin.HandleFunc("GET /api/admin/recordings/{id}/packages/{packageID}", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminRecordingPackageStatus)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/packages/{packageID}/sign", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminSignRecordingPackage)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/packages/{packageID}/complete", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminCompleteRecordingPackage)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/captures", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminCreateRecordingCapture)))
	admin.HandleFunc("GET /api/admin/recordings/{id}/captures/{captureId}", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminGetRecordingCapture)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/captures/{captureId}/objects", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminDeclareRecordingCapture)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/captures/{captureId}/sign", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminSignRecordingCapture)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/captures/{captureId}/confirm", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminConfirmRecordingCapture)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/captures/{captureId}/seal", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminSealRecordingCapture)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/captures/{captureId}/abort", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminAbortRecordingCapture)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/captures/{captureId}/stop", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminLocalRecordingCapture)))
	admin.HandleFunc("DELETE /api/admin/recordings/{id}/captures/{captureId}/auto-publish", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminLocalRecordingCapture)))
	admin.HandleFunc("DELETE /api/admin/recordings/{id}/captures/{captureId}/live", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminLocalRecordingCapture)))
	admin.HandleFunc("POST /api/admin/recordings/{id}/unpublish", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminUnpublishRecording)))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/translation-previews/{targetLocale}", requireScope("cms:bulletins:write", h.adminBulletinTranslationPreview))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/notification-preview", requireScope("cms:bulletins:read", h.previewBulletinNotification))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/notifications", requireScopes([]string{"cms:bulletins:read", "campaigns:write", "campaigns:send"}, h.submitBulletinNotification))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/translation-previews/{targetLocale}", requireContentScope("write", h.adminContentTranslationPreview))
	admin.HandleFunc("POST /api/admin/campaigns/{campaignID}/translation-previews/{targetLocale}", requireScope("campaigns:write", h.adminCampaignTranslationPreview))
	admin.HandleFunc("POST /api/admin/campaign-schedules/{scheduleID}/translation-previews/{targetLocale}", requireScope("campaigns:write", h.adminCampaignScheduleTranslationPreview))
	if h.content != nil {
		h.contentRoutes(public, admin)
		me := http.NewServeMux()
		me.HandleFunc("/api/me/statements/{statementID}/dismissal", h.statementDismissal)
		trusted := requireTrusted(h.trustedCaller, h.daprAPIToken, h.allowDevCaller, me)
		mux.Handle("/api/me/statements/", privateNoStore(h.gatewayPrivateRoutes(me, trusted, trusted)))
	}
	if h.siteSettings != nil {
		h.siteSettingsRoutes(public, admin)
	}
	if h.engagement != nil {
		admin.HandleFunc("POST /api/admin/campaign-submissions", requireScopes([]string{"campaigns:write", "campaigns:send"}, h.submitCustomNotifications))
		admin.HandleFunc("GET /api/admin/campaigns", requireScope("campaigns:read", h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/notification-summaries", requireScope("campaigns:read", h.forwardEngagement))
		admin.HandleFunc("POST /api/admin/campaigns", requireScope("campaigns:write", h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/campaigns/{campaignID}", requireScope("campaigns:read", h.forwardEngagement))
		admin.HandleFunc("PUT /api/admin/campaigns/{campaignID}", requireScope("campaigns:write", h.forwardEngagement))
		admin.HandleFunc("DELETE /api/admin/campaigns/{campaignID}", requireScope("campaigns:write", h.forwardEngagement))
		admin.HandleFunc("POST /api/admin/campaigns/{campaignID}/send", requireScope("campaigns:send", h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/campaigns/{campaignID}/deliveries", requireScope("campaigns:read", h.forwardEngagement))
		admin.HandleFunc("POST /api/admin/campaigns/{campaignID}/retry-failed", requireScope("campaigns:send", h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/campaign-schedules", requireScope("campaigns:read", h.forwardEngagement))
		admin.HandleFunc("POST /api/admin/campaign-schedules", requireCampaignScheduleCreate(h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/campaign-schedules/{scheduleID}", requireScope("campaigns:read", h.forwardEngagement))
		admin.HandleFunc("PUT /api/admin/campaign-schedules/{scheduleID}", requireCampaignScheduleUpdate(h.forwardEngagement))
		admin.HandleFunc("DELETE /api/admin/campaign-schedules/{scheduleID}", requireScope("campaigns:write", h.forwardEngagement))
	}
	trustedAdmin := requireTrusted(h.trustedCaller, h.daprAPIToken, h.allowDevCaller, admin)
	mux.Handle("/api/admin/", privateNoStore(h.gatewayPrivateRoutes(admin, trustedAdmin, trustedAdmin)))
	member := http.NewServeMux()
	h.registerBroadcastRoutes(admin, member)
	h.registerRecordingInteractionRoutes(admin, member)
	h.registerRecordingProgressRoutes(member)
	h.registerIngestRoutes(mux)
	member.HandleFunc("GET /api/member/bulletins/online", h.requireOnlineDiscoveryEntitlements(h.memberOnlineDiscovery))
	member.HandleFunc("POST /api/member/bulletins/{issueID}/versions/{locale}/online/access", h.requireOnlineBulletinEntitlement(h.memberOnlineAccess))
	member.HandleFunc("GET /api/member/bulletins/{issueID}/versions/{locale}/online/reader/state", h.requireOnlineBulletinEntitlement(h.memberReaderState))
	member.HandleFunc("POST /api/member/bulletins/{issueID}/versions/{locale}/online/reader/mutations", h.requireOnlineBulletinEntitlement(h.memberReaderMutations))
	member.HandleFunc("POST /api/member/bulletin-download-jobs", h.requireBulletinEntitlement(h.createBulletinDownloadJob))
	member.HandleFunc("GET /api/member/bulletin-download-jobs/{id}", h.requireBulletinEntitlement(h.getBulletinDownloadJob))
	member.HandleFunc("GET /api/member/bulletin-download-jobs/{id}/file", h.requireBulletinEntitlement(h.downloadPreparedBulletin))
	member.HandleFunc("GET /api/member/bulletins", h.requireBulletinEntitlement(h.listProtectedBulletins))
	member.HandleFunc("GET /api/member/bulletins/latest", h.requireBulletinEntitlement(h.latestProtectedBulletin))
	member.HandleFunc("GET /api/member/bulletins/{issueID}/versions/{locale}", h.requireBulletinEntitlement(h.getProtectedBulletin))
	member.HandleFunc("GET /api/member/bulletins/{issueID}/versions/{locale}/download", h.requireBulletinEntitlement(h.downloadProtectedBulletin))
	member.HandleFunc("GET /api/member/recordings/live", h.requireRecordingEntitlement(h.memberListLiveRecordings))
	member.HandleFunc("GET /api/member/recordings/{id}/live/{captureId}/cover", h.requireRecordingEntitlement(h.memberLiveCoverContent))
	member.HandleFunc("POST /api/member/recordings/{id}/live/playback", h.requireRecordingEntitlement(h.memberLiveRecordingPlayback))
	member.HandleFunc("GET /api/member/recordings", h.requireRecordingEntitlement(h.memberListRecordings))
	member.HandleFunc("GET /api/member/recordings/{id}/cover", h.requireRecordingEntitlement(h.memberRecordingCoverContent))
	member.HandleFunc("POST /api/member/recordings/{id}/playback", h.requireRecordingEntitlement(h.memberRecordingPlayback))
	lineCaller := map[string]bool{"hhc-line-function-bot": true}
	trustedMember := requireTrustedCallers(map[string]bool{h.trustedCaller: true, "hhc-line-function-bot": true}, h.daprAPIToken, h.allowDevCaller, member)
	directLineBulletins := h.privateServiceOrLegacy(http.MethodGet, lineCaller, trustedMember, trustedMember)
	gatewayMember := h.gatewayPrivateRoutes(member, trustedMember, trustedMember)
	mux.Handle("/api/member/", privateNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/member/bulletins/online" && r.Header.Get(privateServiceCallerHeader) == "hhc-line-function-bot" {
			directLineBulletins.ServeHTTP(w, r)
			return
		}
		gatewayMember.ServeHTTP(w, r)
	})))
	private := http.NewServeMux()
	lineAccess := requireTrustedCallers(lineCaller, h.daprAPIToken, h.allowDevCaller, http.HandlerFunc(h.lineMemberAccess))
	private.Handle("GET /priv/line/member-access", h.privateServiceOrLegacy(http.MethodGet, lineCaller, lineAccess, lineAccess))
	h.legalRoutes(public, admin, member, private)
	private.Handle("POST /priv/account-cleanup", h.requirePrivateService(http.MethodPost, map[string]bool{"account-api": true}, http.HandlerFunc(h.cleanupWatermarkAccount)))
	private.Handle("GET /priv/bulletins/{issueID}/notification-policy", requireServiceCaller(map[string]bool{"engagement-api": true}, h.daprAPIToken, h.allowDevCaller, http.HandlerFunc(h.bulletinNotificationPolicy)))
	private.Handle("GET /priv/recordings/{recordingID}/notification-policy", h.requirePrivateService(http.MethodGet, map[string]bool{"engagement-api": true}, http.HandlerFunc(h.recordingNotificationPolicy)))
	if h.gatewayLocationsToken != "" {
		private.Handle("GET "+gatewayLocationsPath, publicReadDeadline(http.HandlerFunc(h.publicLocations)))
	}
	mux.Handle("/priv/", privateNoStore(private))
	routes := requestID(accessLog(captureErrors(mux)))
	if wrap != nil {
		routes = wrap(routes)
	}
	if h.gatewayLocationsToken != "" {
		routes = gatewayLocationsGuard(h.gatewayLocationsToken, routes)
	}
	if h.daprDisabled {
		routes = rejectLegacyCaller(routes)
	}
	return routes
}

func (h *Handler) forwardEngagement(w http.ResponseWriter, r *http.Request) {
	path := "/priv" + strings.TrimPrefix(r.URL.Path, "/api/admin")
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	var body io.Reader
	if r.Body != nil && r.Body != http.NoBody {
		body = http.MaxBytesReader(w, r.Body, maxAdminProxyBody)
	}
	response, err := h.engagement.Forward(r.Context(), r.Method, path, body, actor(r), r.Header.Get("X-HHC-Request-ID"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "engagement_service_unavailable", "Campaign service is unavailable.")
		return
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 2<<20))
}

func (h *Handler) adminAuditProvenance(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(principalKey{}).(principal)
		if h.auditOutbox == nil {
			if ok && p.ActorType == "service" {
				writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
				return
			}
			next(w, r)
			return
		}
		identityHeader := "X-HHC-User-ID"
		if p.ActorType == "service" {
			identityHeader = "X-HHC-Principal-ID"
		}
		users, requests := r.Header.Values(identityHeader), r.Header.Values("X-HHC-Request-ID")
		if !ok || len(users) != 1 || len(requests) != 1 || users[0] != actor(r) || requests[0] != p.RequestID || !auditclient.ValidActorRequest(actor(r), p.RequestID) {
			writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
			return
		}
		next(w, r)
	}
}

func (h *Handler) enqueueAdminAudit(w http.ResponseWriter, r *http.Request, action, resourceID string, metadata auditclient.Metadata) bool {
	if h.auditOutbox == nil {
		return true
	}
	if h.auditNow == nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
		return false
	}
	p, ok := r.Context().Value(principalKey{}).(principal)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
		return false
	}
	now := h.auditNow().UTC()
	event, err := auditclient.NewEventForPrincipal(action, resourceID, p.ActorType, actor(r), p.RequestID, now, metadata)
	if err == nil {
		_, err = h.auditOutbox.Enqueue(r.Context(), event, now)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
		return false
	}
	return true
}

type createUploadInput struct {
	Series    string `json:"series,omitempty"`
	Locale    string `json:"locale"`
	FileName  string `json:"fileName"`
	MIMEType  string `json:"mimeType"`
	SizeBytes int64  `json:"sizeBytes"`
}
type completeUploadInput struct {
	AutoPublish       bool   `json:"autoPublish,omitempty"`
	NotifySubscribers bool   `json:"notifySubscribers,omitempty"`
	Series            string `json:"series,omitempty"`
	Locale            string `json:"locale"`
	Title             string `json:"title"`
	Subtitle          string `json:"subtitle"`
	FileName          string `json:"fileName"`
	MIMEType          string `json:"mimeType"`
	SizeBytes         int64  `json:"sizeBytes"`
	ChecksumSHA256    string `json:"checksumSha256"`
}
type updateVersionInput struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
}

type assetStatusResponse struct {
	ID               string `json:"id"`
	UploadStatus     string `json:"uploadStatus"`
	ScanStatus       string `json:"scanStatus"`
	ProcessingStatus string `json:"processingStatus"`
	Retryable        bool   `json:"retryable"`
}

func cmsAssetStatus(asset assetclient.Asset) assetStatusResponse {
	return assetStatusResponse{
		ID: asset.ID, UploadStatus: asset.UploadStatus, ScanStatus: asset.ScanStatus,
		ProcessingStatus: asset.ProcessingStatus, Retryable: asset.ScanStatus == "failed",
	}
}

func (h *Handler) adminCreateUpload(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	var input createUploadInput
	if !decode(w, r, &input) {
		return
	}
	edition, valid := bulletins.NormalizeBulletinEdition(strings.TrimSpace(input.Series), strings.TrimSpace(input.Locale))
	if !valid || strings.TrimSpace(input.FileName) == "" || input.MIMEType != "application/pdf" || input.SizeBytes <= 0 || input.SizeBytes > maxBulletinPDFSize || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	_, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	created, err := h.uploads.CreateBulletinUpload(r.Context(), r.PathValue("issueID"), input.Locale, input.FileName, input.MIMEType, input.SizeBytes, string(edition.Series)+":"+r.Header.Get("Idempotency-Key"))
	if err != nil {
		handleError(w, err)
		return
	}
	logAssetEvent(r, "asset upload session created", "bulletin", r.PathValue("issueID"), created.Asset.ID)
	writeData(w, http.StatusCreated, created, nil)
}
func (h *Handler) adminCompleteUpload(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input completeUploadInput
	if !decode(w, r, &input) {
		return
	}
	edition, valid := bulletins.NormalizeBulletinEdition(strings.TrimSpace(input.Series), strings.TrimSpace(input.Locale))
	if !valid || strings.TrimSpace(input.Title) == "" || input.MIMEType != "application/pdf" || input.SizeBytes <= 0 || len(input.ChecksumSHA256) != 64 {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	if !allowBulletinAutomaticPublish(w, r, input.AutoPublish, input.NotifySubscribers, string(edition.Series), input.Locale) {
		return
	}
	_, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	assetID := r.PathValue("assetID")
	asset, err := h.uploads.Get(r.Context(), assetID)
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	if err != nil || !ownedBulletinAsset(asset, r.PathValue("issueID"), assetID, input.Locale) {
		writeError(w, http.StatusForbidden, "asset_owner_mismatch", "The uploaded asset does not belong to this bulletin.")
		return
	}
	asset, err = h.uploads.CompleteUpload(r.Context(), assetID, assetclient.CompleteUploadInput{SizeBytes: input.SizeBytes, ChecksumSHA256: input.ChecksumSHA256, MIMEType: input.MIMEType})
	if err != nil {
		handleError(w, err)
		return
	}
	if !ownedBulletinAsset(asset, r.PathValue("issueID"), assetID, input.Locale) {
		writeError(w, http.StatusForbidden, "asset_owner_mismatch", "The uploaded asset does not belong to this bulletin.")
		return
	}
	if _, err = h.uploads.CreateServiceGrant(r.Context(), asset.ID, "bulletin-source:"+asset.ID); err != nil {
		handleError(w, err)
		return
	}
	fileName := asset.OriginalFileName
	if fileName == "" {
		fileName = input.FileName
	}
	value, err := h.service.PutVersion(r.Context(), r.PathValue("issueID"), expected, bulletins.PutVersionInput{Series: string(edition.Series), Locale: input.Locale, Title: input.Title, Subtitle: input.Subtitle, PDFAssetID: asset.ID, PDFFileName: fileName, AutoPublish: input.AutoPublish, NotifySubscribers: input.NotifySubscribers}, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	logAssetEvent(r, "asset attached", "bulletin", r.PathValue("issueID"), asset.ID)
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminAssetStatus(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	asset, err := h.uploads.Get(r.Context(), r.PathValue("assetID"))
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	if err != nil || asset.ID != r.PathValue("assetID") || asset.Namespace != "cms.weekly.pdf" ||
		asset.OwnerService != "hhc-web-api" || asset.OwnerType != "bulletin_issue" || asset.OwnerID != r.PathValue("issueID") {
		writeError(w, http.StatusNotFound, "not_found", "The bulletin asset was not found.")
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.asset_status.read", asset.ID, auditclient.Metadata{}) {
		return
	}
	writeData(w, http.StatusOK, cmsAssetStatus(asset), nil)
}
func (h *Handler) adminRetryAssetScan(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	asset, err := h.uploads.Get(r.Context(), r.PathValue("assetID"))
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	if err != nil || asset.ID != r.PathValue("assetID") || asset.Namespace != "cms.weekly.pdf" ||
		asset.OwnerService != "hhc-web-api" || asset.OwnerType != "bulletin_issue" || asset.OwnerID != r.PathValue("issueID") {
		writeError(w, http.StatusNotFound, "not_found", "The bulletin asset was not found.")
		return
	}
	if asset.ScanStatus != "failed" {
		writeError(w, http.StatusConflict, "asset_not_retryable", "The asset scan cannot be retried.")
		return
	}
	if err := h.uploads.RequeueScan(r.Context(), asset.ID); err != nil {
		if errors.Is(err, assetclient.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		} else {
			writeError(w, http.StatusConflict, "asset_not_retryable", "The asset scan state changed.")
		}
		return
	}
	asset.ScanStatus = "pending"
	writeData(w, http.StatusOK, cmsAssetStatus(asset), nil)
}
func ownedBulletinAsset(asset assetclient.Asset, issueID, assetID, locale string) bool {
	return asset.ID == assetID && asset.Namespace == "cms.weekly.pdf" &&
		asset.OwnerService == "hhc-web-api" && asset.OwnerType == "bulletin_issue" &&
		asset.OwnerID == issueID && asset.Locale == locale
}
func logAssetEvent(r *http.Request, message, resourceType, resourceID, assetID string) {
	slog.Info(message,
		"request_id", r.Header.Get("X-HHC-Request-ID"),
		"resource_type", resourceType,
		"resource_id", resourceID,
		"asset_id", assetID,
	)
}
func (h *Handler) ready(w http.ResponseWriter, _ *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Application is not initialized.")
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ready"}, nil)
}

func (h *Handler) adminList(w http.ResponseWriter, r *http.Request) {
	page, size := pagination(r)
	value, err := h.service.ListIssues(r.Context(), page, size, r.URL.Query().Get("status"), r.URL.Query().Get("q"), r.URL.Query().Get("sort"), r.URL.Query().Get("direction"))
	if err != nil {
		handleError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.list", "", auditclient.Metadata{}) {
		return
	}
	writeData(w, http.StatusOK, value.Items, map[string]any{"page": value.Page, "pageSize": value.PageSize, "total": value.Total})
}
func (h *Handler) adminGet(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.read", value.ID, auditclient.Metadata{}) {
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminUpdateIssue(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.UpdateIssueInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.UpdateIssue(r.Context(), r.PathValue("issueID"), expected, input, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminCreate(w http.ResponseWriter, r *http.Request) {
	var input bulletins.CreateIssueInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.CreateIssue(r.Context(), input, actor(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusCreated, value, nil)
}
func (h *Handler) adminPublish(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.PublishInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.PublishSelection(r.Context(), r.PathValue("issueID"), input, expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, value, nil)
}
func (h *Handler) adminUnpublish(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.UnpublishInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.UnpublishSelection(r.Context(), r.PathValue("issueID"), input, expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminDelete(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteIssue(r.Context(), r.PathValue("issueID"), expected, actor(r)); err != nil {
		handleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) adminUpdateVersion(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input updateVersionInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.UpdateVersion(r.Context(), r.PathValue("issueID"), bulletinSeries(r), r.PathValue("locale"), expected, bulletins.UpdateVersionInput{Title: input.Title, Subtitle: input.Subtitle}, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminDeleteVersion(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	value, err := h.service.DeleteVersion(r.Context(), r.PathValue("issueID"), bulletinSeries(r), r.PathValue("locale"), expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminIssueRevisions(w http.ResponseWriter, r *http.Request) {
	values, err := h.service.IssueRevisions(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	resourceID := r.PathValue("issueID")
	if parsed, parseErr := uuid.Parse(resourceID); parseErr == nil {
		resourceID = parsed.String()
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.revisions.read", resourceID, auditclient.Metadata{}) {
		return
	}
	writeData(w, http.StatusOK, values, nil)
}
func (h *Handler) adminRestoreIssueRevision(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || revision < 1 {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	values, err := h.service.IssueRevisions(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	var snapshot *bulletins.Issue
	for index := range values {
		if values[index].Version == revision {
			snapshot = &values[index].Snapshot
			break
		}
	}
	if snapshot == nil {
		handleError(w, bulletins.ErrNotFound)
		return
	}
	for _, version := range snapshot.Versions {
		if _, ok := bulletins.NormalizeBulletinEdition(version.Series, version.Locale); !ok {
			handleError(w, bulletins.ErrInvalid)
			return
		}
	}
	if h.uploads == nil {
		handleError(w, assetclient.ErrUnavailable)
		return
	}
	for _, version := range snapshot.Versions {
		asset, assetErr := h.uploads.Get(r.Context(), version.PDFAssetID)
		if assetErr != nil || !ownedBulletinAsset(asset, r.PathValue("issueID"), version.PDFAssetID, version.Locale) {
			if errors.Is(assetErr, assetclient.ErrUnavailable) {
				handleError(w, assetErr)
			} else {
				handleError(w, bulletins.ErrNotPublishable)
			}
			return
		}
	}
	value, err := h.service.RestoreIssueRevision(r.Context(), r.PathValue("issueID"), revision, expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func locale(r *http.Request) string {
	value := r.URL.Query().Get("locale")
	if value == "" {
		value = "zh-Hant"
	}
	return value
}
func bulletinSeries(r *http.Request) string {
	value := r.URL.Query().Get("series")
	if value == "" {
		value = "general"
	}
	return value
}
func pagination(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	return page, size
}
func ifMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "A valid If-Match version is required.")
		return 0, false
	}
	return value, true
}
func decode(w http.ResponseWriter, r *http.Request, destination any) bool {
	return decodeLimit(w, r, destination, 1<<20)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, destination any, limit int64) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	trimmed := bytes.TrimSpace(body)
	if err != nil || !utf8.Valid(body) || len(trimmed) == 0 || trimmed[0] != '{' {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return false
	}
	return true
}
func handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assetclient.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
	case errors.Is(err, bulletins.ErrAuditUnavailable):
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
	case errors.Is(err, bulletins.ErrOnlinePublicationUnavailable):
		writeError(w, http.StatusServiceUnavailable, "online_publication_unavailable", "Online publication controls are not enabled.")
	case errors.Is(err, bulletins.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
	case errors.Is(err, bulletins.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The bulletin was not found.")
	case errors.Is(err, bulletins.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "The request conflicts with an existing resource.")
	case errors.Is(err, bulletins.ErrOnlineDocumentExists):
		writeError(w, http.StatusConflict, "online_document_exists", "Online bulletin history prevents deletion of this issue or edition.")
	case errors.Is(err, bulletins.ErrPrecondition):
		writeError(w, http.StatusPreconditionFailed, "version_conflict", "The resource changed. Reload and try again.")
	case errors.Is(err, bulletins.ErrNotPublishable):
		writeError(w, http.StatusUnprocessableEntity, "not_publishable", "The bulletin is not ready for this operation.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The service could not complete the request.")
	}
}

type envelope struct {
	Data  any `json:"data"`
	Meta  any `json:"meta"`
	Error any `json:"error"`
}

func writeData(w http.ResponseWriter, status int, data, meta any) {
	if meta == nil {
		meta = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: data, Meta: meta, Error: nil})
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	recordErrorCode(w, code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: nil, Meta: map[string]any{}, Error: map[string]string{"code": code, "message": message}})
}
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ""
		if values := r.Header.Values("X-HHC-Request-ID"); len(values) == 1 && auditclient.ValidRequestID(values[0]) {
			id = values[0]
		}
		if id == "" {
			id = strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		w.Header().Set("X-HHC-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status               int
	applicationErrorCode string
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		started := time.Now()
		response := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(response, r)
		if response.status == 0 {
			response.status = http.StatusOK
		}
		duration := time.Since(started)
		if response.status < 400 && duration < time.Second && (r.URL.Path == "/health" || r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" || r.URL.Path == "/ready" || os.Getenv("ACCESS_LOG_MODE") == "errors") {
			return
		}
		path := r.Pattern
		if path == "" {
			path = "/unmatched"
		}
		logging.Access.Info("http request",
			"request_id", w.Header().Get("X-HHC-Request-ID"),
			"method", r.Method,
			"path", path,
			"status", response.status,
			"application_error_code", response.applicationErrorCode,
			"duration_ms", duration.Milliseconds(),
		)
	})
}

// Record the application classification before wire adapters translate it; never capture bodies.
func recordErrorCode(w http.ResponseWriter, code string) {
	for {
		if recorder, ok := w.(*statusWriter); ok {
			recorder.applicationErrorCode = code
			return
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = unwrapper.Unwrap()
	}
}
