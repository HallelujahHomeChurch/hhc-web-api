package bulletinwatermark

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"time"
)

const Retention = 365 * 24 * time.Hour
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var encoding = base32.NewEncoding(alphabet).WithPadding(base32.NoPadding)
var ErrCodeCollision = errors.New("watermark trace collision")
var ErrRateLimited = errors.New("watermark lookup rate exceeded")
var ErrInvalidCode = errors.New("invalid trace code")
var ErrNotFound = errors.New("watermark receipt not found")
var ErrRestricted = errors.New("watermark subject restricted")
var ErrInvalidFingerprint = errors.New("invalid watermark reconstruction metadata")
var ErrCandidateLimit = errors.New("watermark candidate limit exceeded")
var ErrReceiptIntegrity = errors.New("watermark exact receipt integrity violation")

// Receipt records issuance, not proof of a completed download or who shared it.
type PageMetadata struct {
	Page         int    `json:"page"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	SourceSHA256 string `json:"source_sha256"`
	OutputSHA256 string `json:"output_sha256"`
}

type Receipt struct {
	Channel            string         `json:"channel,omitempty"`
	FingerprintPayload string         `json:"fingerprintPayload,omitempty"`
	ModelSHA256        string         `json:"modelSha256,omitempty"`
	Pages              []PageMetadata `json:"pages,omitempty"`
	ID                 string         `json:"receiptId"`
	Code               string         `json:"-"`
	UserID             string         `json:"userId"`
	IssueID            string         `json:"issueId"`
	Series             string         `json:"series"`
	Locale             string         `json:"locale"`
	Revision           int64          `json:"revision"`
	SourceAssetID      string         `json:"sourceAssetId"`
	SourceSHA256       string         `json:"sourceSha256"`
	OutputSHA256       string         `json:"outputSha256"`
	RendererVersion    string         `json:"rendererVersion"`
	PrivacyVersion     string         `json:"privacyNoticeVersion"`
	IssuedAt           time.Time      `json:"issuedAt"`
	ExpiresAt          time.Time      `json:"expiresAt"`
}

// NewCode issues 80 random bits plus the existing two-symbol checksum.
func NewCode() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	code := encoding.EncodeToString(b[:])
	return code + checksum(code), nil
}
func checksum(code string) string {
	sum := sha256.Sum256([]byte(code))
	return string([]byte{alphabet[sum[0]>>3], alphabet[((sum[0]&7)<<2)|(sum[1]>>6)]})
}

// NormalizeCode retains exact lookup of legacy 128-bit receipts.
func NormalizeCode(input string) (string, error) {
	if len(input) > 40 {
		return "", ErrInvalidCode
	}
	code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(input), "-", ""))
	if len(code) != 18 && len(code) != 28 {
		return "", ErrInvalidCode
	}
	for _, c := range code {
		if !strings.ContainsRune(alphabet, c) {
			return "", ErrInvalidCode
		}
	}
	payload := code[:len(code)-2]
	raw, err := encoding.DecodeString(payload)
	if err != nil || (len(raw) != 10 && len(raw) != 16) || encoding.EncodeToString(raw) != payload || checksum(payload) != code[len(code)-2:] {
		return "", ErrInvalidCode
	}
	return code, nil
}
func FormatCode(code string) string {
	var groups []string
	for len(code) > 4 {
		groups = append(groups, code[:4])
		code = code[4:]
	}
	return strings.Join(append(groups, code), "-")
}

// NewFingerprint creates an independent payload; it never derives from an account or trace code.
func NewFingerprint() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	var payload [61]byte
	for i := range payload {
		payload[i] = '0' + ((random[i/8] >> (7 - i%8)) & 1)
	}
	return string(payload[:]), nil
}

// ValidateFingerprint permits only complete reconstruction metadata or an entirely legacy receipt.
func (v Receipt) ValidateFingerprint() error {
	if v.FingerprintPayload == "" && v.ModelSHA256 == "" && len(v.Pages) == 0 {
		return nil
	}
	validHash := func(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }
	if len(v.FingerprintPayload) != 61 || strings.Trim(v.FingerprintPayload, "01") != "" || !validHash(v.ModelSHA256) || len(v.Pages) < 1 || len(v.Pages) > 60 {
		return ErrInvalidFingerprint
	}
	for i, p := range v.Pages {
		if p.Page != i+1 || p.Width < 1 || p.Height < 1 || p.Width > 20_000_000 || p.Height > 20_000_000 || int64(p.Width)*int64(p.Height) > 20_000_000 || !validHash(p.SourceSHA256) || !validHash(p.OutputSHA256) {
			return ErrInvalidFingerprint
		}
	}
	return nil
}
