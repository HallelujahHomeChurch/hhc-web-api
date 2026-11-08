package auditclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

//go:embed hhc-web-producer-fixture.json
var fixtureFiles embed.FS

var ErrInvalidEvent = errors.New("invalid audit event")
var accountID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[47][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var hashID = regexp.MustCompile(`^[0-9a-f]{64}$`)
var assetID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var requestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var traceID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Metadata map[string]any
type Provenance struct {
	ActorType    string
	ActorID      string
	RequestID    string
	CredentialID string
}
type provenanceKey struct{}

func WithProvenance(ctx context.Context, actorID, requestID string) context.Context {
	return WithPrincipalProvenance(ctx, "user", actorID, requestID)
}
func WithPrincipalProvenance(ctx context.Context, actorType, actorID, requestID string, credentialID ...string) context.Context {
	p := Provenance{ActorType: actorType, ActorID: actorID, RequestID: requestID}
	if len(credentialID) == 1 {
		p.CredentialID = credentialID[0]
	}
	return context.WithValue(ctx, provenanceKey{}, p)
}
func ProvenanceFromContext(ctx context.Context) (Provenance, bool) {
	value, ok := ctx.Value(provenanceKey{}).(Provenance)
	return value, ok
}
func ValidRequestID(requestID string) bool { return validRequest(requestID) }

func ValidActorRequest(actorID, requestID string) bool {
	return accountID.MatchString(actorID) && validRequest(requestID)
}

type Action struct {
	Action                 string   `json:"action"`
	ActorTypes             []string `json:"actorTypes"`
	ServicePrincipals      []string `json:"servicePrincipals"`
	ActorIDGrammar         string   `json:"actorIdGrammar"`
	Category               string   `json:"category"`
	ResourceOwnerService   string   `json:"resourceOwnerService"`
	ResourceType           string   `json:"resourceType"`
	ResourceIDGrammar      string   `json:"resourceIdGrammar"`
	EventIDGrammar         string   `json:"eventIdGrammar"`
	IdentityStrategy       string   `json:"identityStrategy"`
	Outcome                string   `json:"outcome"`
	Severity               string   `json:"severity"`
	MetadataClassification string   `json:"metadataClassification"`
	RetentionClass         string   `json:"retentionClass"`
	MetadataSchema         string   `json:"metadataSchema"`
}
type Fixture struct {
	SchemaVersion       int             `json:"schemaVersion"`
	SourceService       string          `json:"sourceService"`
	Actions             json.RawMessage `json:"actions"`
	MetadataSchemas     json.RawMessage `json:"metadataSchemas"`
	IDGrammars          json.RawMessage `json:"idGrammars"`
	DenialBindings      json.RawMessage `json:"denialBindings"`
	CredentialFixtures  json.RawMessage `json:"credentialFixtures"`
	CredentialPredicate string          `json:"credentialPredicate"`
	GrammarFixtures     json.RawMessage `json:"grammarFixtures"`
	IdentityFixtures    json.RawMessage `json:"identityFixtures"`
	Checksum            string          `json:"checksum"`
}
type Event struct {
	EventID                string    `json:"eventId"`
	SchemaVersion          int       `json:"schemaVersion"`
	OccurredAt             time.Time `json:"occurredAt"`
	RequestID              string    `json:"requestId"`
	TraceID                string    `json:"traceId,omitempty"`
	SourceService          string    `json:"sourceService"`
	ActorType              string    `json:"actorType"`
	ActorID                string    `json:"actorId"`
	Action                 string    `json:"action"`
	Category               string    `json:"category"`
	ResourceOwnerService   string    `json:"resourceOwnerService"`
	ResourceType           string    `json:"resourceType"`
	ResourceID             string    `json:"resourceId"`
	Outcome                string    `json:"outcome"`
	Severity               string    `json:"severity"`
	MetadataClassification string    `json:"metadataClassification"`
	RetentionClass         string    `json:"retentionClass"`
	Metadata               Metadata  `json:"metadata"`
}

func LoadFixture() (Fixture, error) {
	data, _ := fixtureFiles.ReadFile("hhc-web-producer-fixture.json")
	var fixture Fixture
	if json.Unmarshal(data, &fixture) != nil {
		return Fixture{}, ErrInvalidEvent
	}
	want := fixture.Checksum
	fixture.Checksum = ""
	canonical, _ := json.Marshal(fixture)
	sum := sha256.Sum256(canonical)
	if fixture.SourceService != "hhc-web-api" || want == "" || "sha256:"+hex.EncodeToString(sum[:]) != want {
		return Fixture{}, errors.New("audit fixture checksum mismatch")
	}
	var verified Fixture
	err := json.Unmarshal(data, &verified)
	return verified, err
}
func (f Fixture) contract(action string) (Action, bool) {
	for _, value := range f.Contracts() {
		if value.Action == action {
			return value, true
		}
	}
	return Action{}, false
}
func (f Fixture) Contracts() []Action {
	var actions []Action
	_ = json.Unmarshal(f.Actions, &actions)
	return actions
}

func NewEvent(action, resource, actor, request string, at time.Time, metadata Metadata) (Event, error) {
	return NewEventForPrincipal(action, resource, "user", actor, request, at, metadata)
}
func NewEventForPrincipal(action, resource, actorType, actor, request string, at time.Time, metadata Metadata) (Event, error) {
	fixture, err := LoadFixture()
	contract, ok := fixture.contract(action)
	if err != nil || !ok {
		return Event{}, ErrInvalidEvent
	}
	event := Event{SchemaVersion: 1, EventID: uuid.NewString(), OccurredAt: at.UTC(), RequestID: request, SourceService: fixture.SourceService, ActorType: actorType, ActorID: actor, Action: action, Category: contract.Category, ResourceOwnerService: contract.ResourceOwnerService, ResourceType: contract.ResourceType, ResourceID: resource, Outcome: contract.Outcome, Severity: contract.Severity, MetadataClassification: contract.MetadataClassification, RetentionClass: contract.RetentionClass, Metadata: metadata}
	switch contract.IdentityStrategy {
	case "admin_access_sha256":
		event.EventID = identityHash("admin-access", action, actor, request, resource)
	case "admin_asset_status_utc_5minute_sha256":
		event.OccurredAt = at.UTC().Truncate(5 * time.Minute)
		event.EventID = identityHash("admin-asset-status", fixture.SourceService, action, actor, contract.ResourceType, resource, event.OccurredAt.Format(time.RFC3339))
	case "mutation_uuid_v4":
	default:
		return Event{}, ErrInvalidEvent
	}
	_, _, err = event.Canonical()
	return event, err
}
func (e Event) Canonical() ([]byte, string, error) {
	fixture, err := LoadFixture()
	contract, ok := fixture.contract(e.Action)
	if err != nil || !ok || e.SchemaVersion != 1 || e.OccurredAt.IsZero() || !validRequest(e.RequestID) || e.TraceID != "" && !traceID.MatchString(e.TraceID) || e.SourceService != fixture.SourceService || !validPrincipalActor(contract, e) || e.Category != contract.Category || e.ResourceOwnerService != contract.ResourceOwnerService || e.ResourceType != contract.ResourceType || e.Outcome != contract.Outcome || e.Severity != contract.Severity || e.MetadataClassification != contract.MetadataClassification || e.RetentionClass != contract.RetentionClass || !validResource(contract.ResourceIDGrammar, e.ResourceID) || !validMetadata(contract.MetadataSchema, e.Metadata) || !validIdentity(contract, e) {
		return nil, "", ErrInvalidEvent
	}
	e.OccurredAt = e.OccurredAt.UTC()
	if e.Metadata == nil {
		e.Metadata = Metadata{}
	}
	payload, err := json.Marshal(e)
	if err != nil || len(payload) > 32<<10 {
		return nil, "", ErrInvalidEvent
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:]), nil
}
func validPrincipalActor(contract Action, event Event) bool {
	if !slices.Contains(contract.ActorTypes, event.ActorType) {
		return false
	}
	if event.ActorType == "service" && strings.HasSuffix(contract.ActorIDGrammar, "service=servicePrincipals") {
		return slices.Contains(contract.ServicePrincipals, event.ActorID)
	}
	return accountID.MatchString(event.ActorID) && (event.ActorType == "user" || event.ActorType == "service" && strings.HasSuffix(contract.ActorIDGrammar, "service=account_uuid"))
}

func Decode(payload []byte) (Event, error) {
	var event Event
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if len(payload) > 32<<10 || decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF {
		return event, ErrInvalidEvent
	}
	var envelope struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if json.Unmarshal(payload, &envelope) != nil || len(bytes.TrimSpace(envelope.Metadata)) == 0 || bytes.TrimSpace(envelope.Metadata)[0] != '{' {
		return event, ErrInvalidEvent
	}
	_, _, err := event.Canonical()
	return event, err
}
func EquivalentForEnqueue(stored, incoming Event) bool {
	storedPayload, storedHash, storedErr := stored.Canonical()
	incomingPayload, incomingHash, incomingErr := incoming.Canonical()
	if storedErr != nil || incomingErr != nil {
		return false
	}
	if storedHash == incomingHash {
		return true
	}
	fixture, _ := LoadFixture()
	contract, ok := fixture.contract(incoming.Action)
	if !ok || stored.EventID != incoming.EventID {
		return false
	}
	switch contract.IdentityStrategy {
	case "admin_access_sha256":
		incoming.OccurredAt = stored.OccurredAt
	case "admin_asset_status_utc_5minute_sha256":
		incoming.RequestID = stored.RequestID
	default:
		return false
	}
	incomingPayload, _, incomingErr = incoming.Canonical()
	return incomingErr == nil && bytes.Equal(storedPayload, incomingPayload)
}
func validIdentity(contract Action, event Event) bool {
	switch contract.IdentityStrategy {
	case "mutation_uuid_v4":
		return contract.EventIDGrammar == "uuid" && uuidV4.MatchString(event.EventID)
	case "admin_access_sha256":
		return contract.EventIDGrammar == "hash" && event.EventID == identityHash("admin-access", event.Action, event.ActorID, event.RequestID, event.ResourceID)
	case "admin_asset_status_utc_5minute_sha256":
		return contract.EventIDGrammar == "hash" && event.OccurredAt.Equal(event.OccurredAt.UTC().Truncate(5*time.Minute)) && event.EventID == identityHash("admin-asset-status", event.SourceService, event.Action, event.ActorID, event.ResourceType, event.ResourceID, event.OccurredAt.UTC().Format(time.RFC3339))
	default:
		return false
	}
}
func validResource(grammar, value string) bool {
	switch grammar {
	case "uuid":
		return uuidV4.MatchString(value)
	case "asset_hex":
		return assetID.MatchString(value)
	case "empty":
		return value == ""
	case "singleton":
		return value == "singleton"
	default:
		return false
	}
}
func validMetadata(schema string, metadata Metadata) bool {
	if metadata == nil {
		metadata = Metadata{}
	}
	switch schema {
	case "recording_capture":
		authorizer, a := metadata["authorizerId"].(string)
		capture, c := metadata["captureId"].(string)
		intent, i := metadata["intentId"].(string)
		return len(metadata) == 3 && a && c && i && accountID.MatchString(authorizer) && assetID.MatchString(capture) && uuidV4.MatchString(intent)
	case "empty":
		return len(metadata) == 0
	case "recording_lookup":
		result, ok := metadata["result"].(string)
		return len(metadata) == 1 && ok && slices.Contains([]string{"matched", "not_found", "invalid", "rate_limited", "restricted"}, result)
	case "content":
		module, ok := metadata["module"].(string)
		return len(metadata) == 1 && ok && (module == "news" || module == "pages")
	case "filters":
		return validFilters(metadata, false)
	case "content_filters":
		return validFilters(metadata, true)
	default:
		return false
	}
}
func validFilters(metadata Metadata, content bool) bool {
	allowed := 1
	if content {
		allowed = 2
		module, ok := metadata["module"].(string)
		if !ok || (module != "news" && module != "pages") {
			return false
		}
	}
	if len(metadata) > allowed {
		return false
	}
	raw, ok := metadata["filterNames"]
	if !ok {
		return !content || len(metadata) == 1
	}
	values, ok := raw.([]string)
	if !ok {
		generic, ok := raw.([]any)
		if !ok {
			return false
		}
		values = make([]string, len(generic))
		for i, value := range generic {
			values[i], ok = value.(string)
			if !ok {
				return false
			}
		}
	}
	return len(values) == 0
}
func validRequest(value string) bool {
	return requestID.MatchString(value) && net.ParseIP(value) == nil && !credentialShaped(value)
}
func credentialShaped(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 && len(parts) != 5 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := base64.RawURLEncoding.DecodeString(part); err != nil {
			return false
		}
	}
	return true
}
func identityHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

type AppendResult struct {
	Result     string
	HTTPStatus int
	DurationMs int64
}
type Client struct {
	http            *http.Client
	endpoint, token string
	privateHTTPS    bool
}

func New(port int, appID, token string, privateHTTPS bool) (*Client, error) {
	if !privateHTTPS && (port < 1 || port > 65535) || appID != "audit-log" && appID != "audit-log-test" || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid audit client configuration")
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/v1.0/invoke/%s/method/priv/audit/events", port, appID)
	if privateHTTPS {
		if appID != "audit-log" {
			return nil, errors.New("invalid private audit application")
		}
		endpoint = "https://audit-log.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io/priv/audit/events"
	}
	client := newClient(&http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint, token)
	client.privateHTTPS = privateHTTPS
	return client, nil
}
func newClient(httpClient *http.Client, endpoint, token string) *Client {
	return &Client{http: httpClient, endpoint: endpoint, token: token}
}
func (c *Client) Append(ctx context.Context, payload []byte) (result AppendResult) {
	started := time.Now()
	defer func() { result.DurationMs = time.Since(started).Milliseconds() }()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return AppendResult{Result: "dead_letter"}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Audit-Token", c.token)
	if c.privateHTTPS {
		request.Header.Set("X-HHC-Audit-Caller", "hhc-web-api")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return AppendResult{Result: "retry"}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	result.HTTPStatus = response.StatusCode
	switch {
	case c.privateHTTPS && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden):
		result.Result = "retry"
	case response.StatusCode == 201:
		result.Result = "accepted"
	case response.StatusCode == 200:
		result.Result = "duplicate"
	case response.StatusCode == 409:
		result.Result = "conflict"
	case response.StatusCode == 429 || response.StatusCode >= 500 && response.StatusCode <= 599:
		result.Result = "retry"
	default:
		result.Result = "dead_letter"
	}
	return result
}

type PipelineStats struct {
	PendingCount         int64
	OldestPendingSeconds float64
	DeadLetterCount      int64
}
