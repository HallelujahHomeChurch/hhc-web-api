package recordings

import (
	"context"
	"crypto/rand"
	"regexp"
	"strings"
	"time"
)

const WatermarkRetention = 365 * 24 * time.Hour
const watermarkAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var watermarkCodePattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{5}-?[0-9A-HJKMNP-TV-Z]{5}$`)

type WatermarkReceipt struct {
	ID            string    `json:"receiptId"`
	Code          string    `json:"code"`
	UserID        string    `json:"userId"`
	RecordingID   string    `json:"recordingId"`
	PackageID     string    `json:"packageId"`
	FormatVersion int       `json:"formatVersion"`
	IssuedAt      time.Time `json:"issuedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

func NormalizeWatermarkCode(input string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(input))
	if !watermarkCodePattern.MatchString(code) {
		return "", ErrInvalid
	}
	return strings.ReplaceAll(code, "-", ""), nil
}
func NewWatermarkCode() (string, error) {
	var bytes [10]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	for i, b := range bytes {
		bytes[i] = watermarkAlphabet[b&31]
	}
	return string(bytes[:]), nil
}
func (s *Service) LookupWatermark(ctx context.Context, actor, recordingID, code string) (WatermarkReceipt, error) {
	if !s.watermarkTraceEnabled {
		return WatermarkReceipt{}, ErrNotFound
	}
	return s.repo.LookupRecordingWatermark(ctx, actor, recordingID, code, s.now())
}

func (s *Service) WithWatermarkTrace(enabled bool) *Service {
	s.watermarkTraceEnabled = enabled
	return s
}
