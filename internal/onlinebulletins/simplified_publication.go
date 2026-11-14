package onlinebulletins

import (
	"encoding/hex"
	"github.com/google/uuid"
	"strings"
	"time"
	"unicode/utf8"
)

// Admin-only operation facts. Publication availability remains PublishedRevision.
type SimplifiedPublicationSource struct {
	DocumentID   string  `json:"documentId"`
	Revision     int64   `json:"revision"`
	ContentHash  *string `json:"contentHash"`
	MetadataHash *string `json:"metadataHash"`
}
type SimplifiedPublicationFailure struct {
	Code     string    `json:"code"`
	Kind     string    `json:"kind"`
	Phase    string    `json:"phase"`
	At       time.Time `json:"at"`
	Attempts int       `json:"attempts"`
}
type SimplifiedPublicationRecovery struct {
	AuthorizationID string    `json:"authorizationId"`
	ReceiptID       string    `json:"receiptId"`
	Revision        int64     `json:"revision"`
	CreatedAt       time.Time `json:"createdAt"`
}
type AdminDerivedPublication struct {
	Recovery        *SimplifiedPublicationRecovery `json:"recovery"`
	OperationID     string                         `json:"operationId"`
	Source          SimplifiedPublicationSource    `json:"source"`
	SourceCurrent   *bool                          `json:"sourceCurrent"`
	Status          string                         `json:"status"`
	Phase           string                         `json:"phase"`
	ConversionJobID *string                        `json:"conversionJobId"`
	ResultRevision  *int64                         `json:"resultRevision"`
	Attempts        *int                           `json:"attempts"`
	FailureCount    *int64                         `json:"failureCount"`
	CreatedAt       *time.Time                     `json:"createdAt"`
	UpdatedAt       *time.Time                     `json:"updatedAt"`
	NextAttemptAt   *time.Time                     `json:"nextAttemptAt"`
	ErrorCode       string                         `json:"errorCode"`
	LastFailure     *SimplifiedPublicationFailure  `json:"lastFailure"`
	History         []SimplifiedPublicationFailure `json:"history"`
	HasMore         bool                           `json:"hasMore"`
	HistoryCoverage string                         `json:"historyCoverage"`
}
type AdminInitialNotification struct {
	ID               string     `json:"id"`
	Generation       int64      `json:"generation"`
	Status           string     `json:"status"`
	Attempts         *int       `json:"attempts"`
	ErrorCode        string     `json:"errorCode"`
	CanonicalVersion *int64     `json:"canonicalVersion"`
	CreatedAt        *time.Time `json:"createdAt"`
	UpdatedAt        *time.Time `json:"updatedAt"`
	NextAttemptAt    *time.Time `json:"nextAttemptAt"`
}
type SimplifiedRecoverySource struct {
	DocumentID    string `json:"documentId"`
	Revision      int64  `json:"revision"`
	ContentHash   string `json:"contentHash"`
	OnlineVersion int64  `json:"onlineVersion"`
}
type SimplifiedRecoveryInput struct {
	TargetDocumentID       string                   `json:"targetDocumentId"`
	CanonicalVersion       int64                    `json:"canonicalVersion"`
	OnlineVersion          int64                    `json:"-"`
	Source                 SimplifiedRecoverySource `json:"source"`
	PublicationOperationID string                   `json:"publicationOperationId"`
	Revision               int64                    `json:"revision"`
	LayoutValidationHash   string                   `json:"layoutValidationHash"`
}
type SimplifiedRecoveryReceipt struct {
	RecoveryReceipt
	Source                 SimplifiedRecoverySource `json:"source"`
	PublicationOperationID string                   `json:"publicationOperationId"`
	AuthorizationID        string                   `json:"authorizationId"`
}
type SimplifiedRecoveryResult struct {
	Receipt     SimplifiedRecoveryReceipt `json:"receipt"`
	Publication ConfirmedRevision         `json:"publication"`
	Replayed    bool                      `json:"replayed"`
}

// Keys are opaque UTF-8 bytes. Do not trim or normalize an operation identity.
func ValidSimplifiedRecoveryKey(key string) bool {
	if !utf8.ValidString(key) || len(key) < 1 || len(key) > 128 || strings.TrimSpace(key) != key {
		return false
	}
	for _, c := range key {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func ValidSimplifiedRecoveryInput(input SimplifiedRecoveryInput) bool {
	validHash := func(s string) bool {
		b, e := hex.DecodeString(s)
		return e == nil && len(b) == 32 && strings.ToLower(s) == s
	}
	return uuid.Validate(input.TargetDocumentID) == nil && uuid.Validate(input.Source.DocumentID) == nil && uuid.Validate(input.PublicationOperationID) == nil && input.CanonicalVersion > 0 && input.OnlineVersion > 0 && input.Source.OnlineVersion > 0 && input.Source.Revision > 0 && input.Revision > 0 && validHash(input.Source.ContentHash) && validHash(input.LayoutValidationHash)
}
