package legal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/google/uuid"
	"regexp"
)

type Scope string

const (
	Common         Scope = "common"
	VerifiedMember Scope = "verified-member"
)

var Locales = []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}
var (
	ErrInvalid  = errors.New("invalid legal content")
	ErrNotFound = errors.New("legal content unavailable")
	ErrConflict = errors.New("legal version conflict")
)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Documents struct {
	Terms   json.RawMessage `json:"terms"`
	Privacy json.RawMessage `json:"privacy"`
}
type Manifest struct {
	Scope                      Scope  `json:"scope"`
	Locale                     string `json:"locale"`
	TermsVersion               string `json:"termsVersion"`
	PrivacyNoticeVersion       string `json:"privacyNoticeVersion"`
	TermsSHA256                string `json:"termsSHA256"`
	PrivacySHA256              string `json:"privacySHA256"`
	CommonTermsVersion         string `json:"commonTermsVersion,omitempty"`
	CommonPrivacyNoticeVersion string `json:"commonPrivacyNoticeVersion,omitempty"`
}
type Snapshot struct {
	SnapshotID string    `json:"snapshotId"`
	Manifest   Manifest  `json:"manifest"`
	Documents  Documents `json:"documents"`
}
type Draft struct {
	Scope                      Scope                `json:"scope"`
	TermsVersion               string               `json:"termsVersion"`
	PrivacyNoticeVersion       string               `json:"privacyNoticeVersion"`
	CommonTermsVersion         string               `json:"commonTermsVersion,omitempty"`
	CommonPrivacyNoticeVersion string               `json:"commonPrivacyNoticeVersion,omitempty"`
	Translations               map[string]Documents `json:"translations"`
	Version                    int64                `json:"version"`
}
type Repository interface {
	Draft(context.Context, Scope) (Draft, error)
	SaveDraft(context.Context, Draft, int64, string) (Draft, error)
	Publish(context.Context, Draft, int64, string) ([]Snapshot, error)
	Current(context.Context, Scope, string) (Snapshot, error)
	Resolve(context.Context, string) (Snapshot, error)
}
type Service struct{ repo Repository }

func NewService(repo Repository) *Service { return &Service{repo: repo} }
func ValidScope(s Scope) bool             { return s == Common || s == VerifiedMember }
func ValidLocale(locale string) bool {
	for _, v := range Locales {
		if v == locale {
			return true
		}
	}
	return false
}
func ValidateDraft(d Draft) error {
	if !ValidScope(d.Scope) || !versionPattern.MatchString(d.TermsVersion) || !versionPattern.MatchString(d.PrivacyNoticeVersion) || len(d.Translations) != len(Locales) {
		return ErrInvalid
	}
	if d.Scope == VerifiedMember && (!versionPattern.MatchString(d.CommonTermsVersion) || !versionPattern.MatchString(d.CommonPrivacyNoticeVersion)) {
		return ErrInvalid
	}
	if d.Scope == Common && (d.CommonTermsVersion != "" || d.CommonPrivacyNoticeVersion != "") {
		return ErrInvalid
	}
	for _, locale := range Locales {
		docs, ok := d.Translations[locale]
		if !ok {
			return ErrInvalid
		}
		if content.ValidatePagePayload("terms-of-use", docs.Terms) != nil || content.ValidatePagePayload("privacy-policy", docs.Privacy) != nil {
			return ErrInvalid
		}
	}
	return nil
}
func Canonical(raw json.RawMessage) (json.RawMessage, string, error) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, "", ErrInvalid
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, "", ErrInvalid
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}
func BuildSnapshots(d Draft) ([]Snapshot, error) {
	if err := ValidateDraft(d); err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(Locales))
	for _, locale := range Locales {
		docs := d.Translations[locale]
		terms, th, _ := Canonical(docs.Terms)
		privacy, ph, _ := Canonical(docs.Privacy)
		out = append(out, Snapshot{SnapshotID: uuid.NewString(), Manifest: Manifest{Scope: d.Scope, Locale: locale, TermsVersion: d.TermsVersion, PrivacyNoticeVersion: d.PrivacyNoticeVersion, TermsSHA256: th, PrivacySHA256: ph, CommonTermsVersion: d.CommonTermsVersion, CommonPrivacyNoticeVersion: d.CommonPrivacyNoticeVersion}, Documents: Documents{terms, privacy}})
	}
	return out, nil
}
func ValidateSnapshot(s Snapshot) error {
	if _, err := uuid.Parse(s.SnapshotID); err != nil {
		return ErrInvalid
	}
	m := s.Manifest
	if !ValidScope(m.Scope) || !ValidLocale(m.Locale) || !versionPattern.MatchString(m.TermsVersion) || !versionPattern.MatchString(m.PrivacyNoticeVersion) {
		return ErrInvalid
	}
	if m.Scope == VerifiedMember && (!versionPattern.MatchString(m.CommonTermsVersion) || !versionPattern.MatchString(m.CommonPrivacyNoticeVersion)) {
		return ErrInvalid
	}
	if m.Scope == Common && (m.CommonTermsVersion != "" || m.CommonPrivacyNoticeVersion != "") {
		return ErrInvalid
	}
	if content.ValidatePagePayload("terms-of-use", s.Documents.Terms) != nil || content.ValidatePagePayload("privacy-policy", s.Documents.Privacy) != nil {
		return ErrInvalid
	}
	_, th, _ := Canonical(s.Documents.Terms)
	_, ph, _ := Canonical(s.Documents.Privacy)
	if th != m.TermsSHA256 || ph != m.PrivacySHA256 {
		return ErrInvalid
	}
	return nil
}
func (s *Service) Draft(ctx context.Context, scope Scope) (Draft, error) {
	if !ValidScope(scope) {
		return Draft{}, ErrInvalid
	}
	return s.repo.Draft(ctx, scope)
}
func (s *Service) SaveDraft(ctx context.Context, d Draft, expected int64, actor string) (Draft, error) {
	if ValidateDraft(d) != nil || expected < 0 || actor == "" {
		return Draft{}, ErrInvalid
	}
	return s.repo.SaveDraft(ctx, d, expected, actor)
}
func (s *Service) Publish(ctx context.Context, scope Scope, expected int64, actor string) ([]Snapshot, error) {
	d, err := s.Draft(ctx, scope)
	if err != nil {
		return nil, err
	}
	if ValidateDraft(d) != nil || actor == "" || expected < 1 {
		return nil, ErrInvalid
	}
	return s.repo.Publish(ctx, d, expected, actor)
}
func (s *Service) Current(ctx context.Context, scope Scope, locale string) (Snapshot, error) {
	if !ValidScope(scope) || !ValidLocale(locale) {
		return Snapshot{}, ErrInvalid
	}
	v, err := s.repo.Current(ctx, scope, locale)
	if err == nil {
		err = ValidateSnapshot(v)
		if err == nil && (v.Manifest.Scope != scope || v.Manifest.Locale != locale) {
			err = ErrInvalid
		}
	}
	return v, err
}
func (s *Service) Resolve(ctx context.Context, id string) (Snapshot, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Snapshot{}, ErrInvalid
	}
	v, err := s.repo.Resolve(ctx, id)
	if err == nil {
		err = ValidateSnapshot(v)
		if err == nil && v.SnapshotID != id {
			err = ErrInvalid
		}
	}
	return v, err
}
