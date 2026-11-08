package recordings

import (
	"testing"
	"time"
)

func TestRecordingVisibilityRequiresCompletionAndStopsAtDeadline(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(720 * time.Hour)
	value := Recording{Status: "published", PackageID: "package", ExpiresAt: &expiry}
	if value.Visible(now) {
		t.Fatal("visible without completion")
	}
	value.UploadedAt = &now
	value.ReadyAt = &now
	if !value.Visible(now) || value.Visible(expiry) || !value.Expired(expiry) {
		t.Fatal("incorrect deadline boundary")
	}
	value.Hidden = true
	if value.Visible(now) {
		t.Fatal("hidden recording visible")
	}
}

func TestLegacySingleFileRecordingIsNeverVisible(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	value := Recording{Status: "published", PrimaryAssetVersionID: "file", UploadedAt: &now, ExpiresAt: &expiry}
	if value.Visible(now) {
		t.Fatal("retired single-file recording is visible")
	}
}

func TestPublishReceiptNeverOverridesExpiryOrChangedVersion(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	receipt := PublishReceipt{PublishedVersion: 2, PackageID: "package"}
	current := Recording{Status: "published", Version: 2, PackageID: "package", ReadyAt: &now, UploadedAt: &now, ExpiresAt: &expiry}
	if NewPublishResult(receipt, current, now).Outcome != "published" || NewPublishResult(receipt, current, expiry).Outcome != "state_changed" {
		t.Fatal("receipt overrides expiry")
	}
	current.Version++
	if NewPublishResult(receipt, current, now).Outcome != "state_changed" {
		t.Fatal("receipt overrides later edit")
	}
}
