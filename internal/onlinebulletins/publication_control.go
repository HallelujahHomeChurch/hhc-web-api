package onlinebulletins

import "time"

// New controls are advertised only after all CMS/worker writers participate in
// the durable intent epoch. Absence/false never grants publication authority.
type AdminCapabilities struct {
	ExplicitOnlinePublication bool `json:"explicitOnlinePublication"`
	QualifiedDraftRecovery    bool `json:"qualifiedDraftRecovery"`
}
type PublicationDisposition struct {
	IntentID         string    `json:"intentId"`
	Epoch            int64     `json:"epoch"`
	Status           string    `json:"status"`
	ReasonCode       string    `json:"reasonCode"`
	ChangedAt        time.Time `json:"changedAt"`
	CanonicalVersion int64     `json:"canonicalVersion"`
	SourceAssetID    string    `json:"sourceAssetId"`
	SourceChecksum   string    `json:"sourceChecksum"`
	DocumentID       string    `json:"documentId,omitempty"`
	Revision         *int64    `json:"revision,omitempty"`
	ReceiptID        string    `json:"receiptId,omitempty"`
}
type RecoveryInput struct {
	CanonicalVersion     int64  `json:"canonicalVersion"`
	OnlineVersion        int64  `json:"-"`
	Revision             int64  `json:"revision"`
	LayoutValidationHash string `json:"layoutValidationHash"`
}
type RecoveryReceipt struct {
	ID                   string    `json:"id"`
	OperationKey         string    `json:"operationKey"`
	DocumentID           string    `json:"documentId"`
	Revision             int64     `json:"revision"`
	CanonicalVersion     int64     `json:"canonicalVersion"`
	OnlineVersion        int64     `json:"onlineVersion"`
	LayoutValidationHash string    `json:"layoutValidationHash"`
	Outcome              string    `json:"outcome"`
	CreatedAt            time.Time `json:"createdAt"`
}
type RecoveryResult struct {
	Receipt     RecoveryReceipt   `json:"receipt"`
	Publication ConfirmedRevision `json:"publication"`
	Replayed    bool              `json:"replayed"`
}
type RecoveryBlockedError struct{ Blockers []string }

func (e *RecoveryBlockedError) Error() string {
	return "saved online draft is not qualified for recovery"
}
