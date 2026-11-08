package assetclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

// WithBulletinSource owns the grant, response body and temporary file for one attempt.
// The consumer must finish using the filename before returning.
func (c *Client) WithBulletinSource(ctx context.Context, source Asset, jobID string, attempt int, consume func(string) error) (err error) {
	stage := "input"
	defer func() {
		if err != nil {
			slog.WarnContext(ctx, "bulletin_source_failed", "stage", stage)
		}
	}()
	if c.caller != "hhc-web-api" || source.ID == "" || source.OwnerID == "" || source.Locale != "zh-Hant" || jobID == "" || attempt < 1 || consume == nil {
		return ErrInvalid
	}
	checksum, decodeErr := hex.DecodeString(source.ChecksumSHA256)
	if decodeErr != nil || len(checksum) != sha256.Size {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stage = "metadata"
	actual, err := c.Get(ctx, source.ID)
	if err != nil {
		return err
	}
	stage = "metadata_validation"
	if actual.ID != source.ID || actual.OwnerService != c.caller || actual.OwnerType != "bulletin_issue" || actual.OwnerID != source.OwnerID || actual.Namespace != "cms.weekly.pdf" || actual.Purpose != "weekly_bulletin" || actual.Locale != source.Locale || actual.Visibility != "private" || actual.ChecksumSHA256 != source.ChecksumSHA256 || actual.UploadStatus != "completed" || actual.ScanStatus != "clean" || (actual.ProcessingStatus != "ready" && actual.ProcessingStatus != "not_required") || actual.DetectedMIMEType != "application/pdf" {
		return ErrInvalid
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	// Manual retries reset attempts; each invocation must own a fresh grant.
	stage = "grant"
	grant, err := c.createServiceGrant(ctx, source.ID, fmt.Sprintf("online-extract:%s:%d:%s", jobID, attempt, platform.NewID()), &expiresAt)
	if err != nil {
		return err
	}
	stage = "grant_validation"
	if grant.ID == "" {
		return ErrUnavailable
	}
	defer func() {
		// Revocation must run even when the parser or parent context is cancelled.
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer stop()
		if revokeErr := c.RevokeGrant(cleanup, source.ID, grant.ID); revokeErr != nil && !errors.Is(revokeErr, ErrNotFound) {
			if err == nil {
				stage = "grant_cleanup"
			} else {
				slog.WarnContext(cleanup, "bulletin_source_failed", "stage", "grant_cleanup")
			}
			err = errors.Join(err, revokeErr)
		}
	}()
	if grant.AssetID != source.ID {
		return ErrUnavailable
	}
	stage = "download"
	response, err := c.AuthorizedDownload(ctx, http.MethodGet, source.ID, "", "")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusForbidden, http.StatusUnauthorized:
		return ErrForbidden
	default:
		return ErrUnavailable
	}
	stage = "header"
	contentType, _, mimeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mimeErr != nil || contentType != "application/pdf" || response.ContentLength > 20<<20 {
		return ErrInvalid
	}
	var header [5]byte
	if _, err = io.ReadFull(response.Body, header[:]); err != nil || string(header[:]) != "%PDF-" {
		return ErrInvalid
	}
	stage = "temporary_file"
	file, err := os.CreateTemp("", "hhc-bulletin-extract-*.pdf")
	if err != nil {
		return err
	}
	defer func() {
		cleanupErr := errors.Join(file.Close(), os.Remove(file.Name()))
		if cleanupErr != nil {
			if err == nil {
				stage = "file_cleanup"
			} else {
				slog.WarnContext(ctx, "bulletin_source_failed", "stage", "file_cleanup")
			}
		}
		err = errors.Join(err, cleanupErr)
	}()
	stage = "stream"
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(io.MultiReader(strings.NewReader(string(header[:])), response.Body), (20<<20)+1))
	if err != nil {
		return fmt.Errorf("%w: source stream interrupted", ErrUnavailable)
	}
	stage = "checksum"
	if n > 20<<20 || hex.EncodeToString(digest.Sum(nil)) != source.ChecksumSHA256 {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage = "consume"
	return consume(file.Name())
}
