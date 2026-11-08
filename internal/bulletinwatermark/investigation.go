package bulletinwatermark

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"
)

var ErrInvalidInvestigation = errors.New("invalid watermark investigation")
var ErrInvestigationConflict = errors.New("watermark investigation idempotency conflict")
var ErrInvestigationLease = errors.New("watermark investigation lease unavailable")

const InvestigationEvidenceTTL = 24 * time.Hour
const InvestigationLease = 10 * time.Minute

type MatchMethod string

const (
	MatchOutputSHA256 MatchMethod = "output_sha256"
	MatchPayload      MatchMethod = "payload"
	MatchCandidate    MatchMethod = "candidate"
)

type InvestigationCompletion struct {
	Outcome          string
	ReasonCode       string
	ReceiptID        string
	MatchMethod      MatchMethod
	Proof            string
	CandidateSetHash string
}

type FindingKind string

const (
	FindingExact        FindingKind = "exact_match"
	FindingDecoded      FindingKind = "decoded_match"
	FindingCandidate    FindingKind = "candidate_match"
	FindingScopeHint    FindingKind = "scope_hint"
	FindingInconclusive FindingKind = "inconclusive"
	FindingFailure      FindingKind = "failure"
)

type Finding struct {
	Kind      FindingKind
	ReceiptID string
	Method    MatchMethod
	Proof     string
	Reason    string
}

type CandidateVersion struct {
	CandidateScope
	PageCount int `json:"pageCount"`
}

type Investigation struct {
	EvidenceSHA256    string           `json:"evidenceSha256"`
	ID                string           `json:"id"`
	ActorID           string           `json:"-"`
	Scope             CandidateScope   `json:"scope"`
	Scopes            []CandidateScope `json:"-"`
	Evidence          []byte           `json:"-"`
	Status            string           `json:"status"`
	Result            string           `json:"result,omitempty"`
	ReceiptID         string           `json:"-"`
	ErrorCode         string           `json:"errorCode,omitempty"`
	LeaseToken        string           `json:"-"`
	LeaseUntil        time.Time        `json:"-"`
	Attempts          int              `json:"-"`
	CandidateSetHash  string           `json:"-"`
	MatchMethod       MatchMethod      `json:"matchMethod,omitempty"`
	CreatedAt         time.Time        `json:"createdAt"`
	ExpiresAt         time.Time        `json:"expiresAt"`
	EvidenceExpiresAt time.Time        `json:"evidenceExpiresAt"`
}

func (v Investigation) CandidateScopes() []CandidateScope {
	if len(v.Scopes) > 0 {
		return v.Scopes
	}
	if v.Scope.IssueID != "" {
		return []CandidateScope{v.Scope}
	}
	return nil
}

type InvestigationView struct {
	Investigation
	Receipt *Receipt `json:"-"`
}

type InvestigationHistoryRow struct {
	ID, ActorID, IssueID, IssueDate, ActualIssueID, ActualIssueDate, Locale, Status, Result, ErrorCode, MatchedAccountID, ReceiptID string
	ActorDisplayName, ActorEmail, MatchedDisplayName, MatchedEmail                                                                  string
	ActorAvailable, MatchedAvailable                                                                                                bool
	IssueNumber, ActualIssueNumber                                                                                                  int
	Revision                                                                                                                        int64
	MatchMethod                                                                                                                     MatchMethod
	CreatedAt, IssuedAt                                                                                                             time.Time
}

type InvestigationHistoryPage struct {
	Items      []InvestigationHistoryRow
	NextCursor string
}

// CandidateSetHash binds one complete immutable receipt set without exposing its IDs.
func CandidateSetHash(receipts []Receipt) string {
	ids := make([]string, len(receipts))
	for i, v := range receipts {
		ids[i] = v.ID
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}
