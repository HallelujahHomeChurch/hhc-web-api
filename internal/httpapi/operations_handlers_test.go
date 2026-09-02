package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operations"
)

func TestPublicMeetingOccurrencesUsesRangeCacheAndETag(t *testing.T) {
	stub := &operationsHandlerStub{occurrences: []operations.Occurrence{{ID: "occurrence-secret", MeetingID: "meeting-secret", MeetingKey: "sunday-service", StartsAt: time.Date(2026, 9, 6, 1, 30, 0, 0, time.UTC), EndsAt: time.Date(2026, 9, 6, 3, 0, 0, 0, time.UTC), Status: operations.OccurrenceScheduled}}}
	handler := operationsTestHandler(stub)
	request := httptest.NewRequest(http.MethodGet, "/api/meeting-occurrences?from=2026-09-01T00:00:00Z&to=2026-09-30T00:00:00Z", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "public, max-age=30" || response.Header().Get("ETag") == "" {
		t.Fatalf("headers=%v", response.Header())
	}
	if !strings.Contains(response.Body.String(), `"occurrenceId":"occurrence-secret"`) {
		t.Fatalf("public body missing stable occurrence id: %s", response.Body.String())
	}
	for _, forbidden := range []string{"meeting-secret", "collectionId", "version"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("public body leaked %q: %s", forbidden, response.Body.String())
		}
	}
	if !stub.occurrenceQuery.PublicOnly {
		t.Fatal("public query was not restricted")
	}
	conditional := httptest.NewRequest(http.MethodGet, request.URL.String(), nil)
	conditional.Header.Set("If-None-Match", response.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	handler.ServeHTTP(notModified, conditional)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional status=%d body=%s", notModified.Code, notModified.Body.String())
	}
}

func TestPublicMeetingScheduleOmitsInactiveShape(t *testing.T) {
	stub := &operationsHandlerStub{meetings: []operations.Meeting{{Key: "weekly", Name: "Weekly", Timezone: "Asia/Taipei", Schedule: operations.Schedule{Type: operations.ScheduleWeekly, DaysOfWeek: []time.Weekday{time.Sunday}, StartTime: "10:00"}, DurationMinutes: 90, Visibility: operations.VisibilityPublic, Status: operations.StatusActive}}}
	response := httptest.NewRecorder()
	operationsTestHandler(stub).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/meetings", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"startsAt"`) {
		t.Fatalf("weekly schedule leaked inactive once field: %s", response.Body.String())
	}
}

func TestPublicMeetingOccurrencesRejectsMoreThanNinetyDays(t *testing.T) {
	handler := operationsTestHandler(&operationsHandlerStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/meeting-occurrences?from=2026-09-01T00:00:00Z&to=2026-12-01T00:00:01Z", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPublicMeetingOccurrencesDefaultsToThirtyDaysAndIncludesCancelled(t *testing.T) {
	stub := &operationsHandlerStub{occurrences: []operations.Occurrence{{ID: "cancelled", MeetingKey: "service", Status: operations.OccurrenceCancelled}}}
	handler := operationsTestHandler(stub)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/meeting-occurrences", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := stub.occurrenceQuery.To.Sub(stub.occurrenceQuery.From); got != 30*24*time.Hour {
		t.Fatalf("default range=%s", got)
	}
}

func TestAuthenticatedMeetingSyncWindowsRequiresTrustedIdentityAndRedacts(t *testing.T) {
	stub := &operationsHandlerStub{windows: []operations.MediaSyncWindow{{StartsAt: time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 9, 6, 3, 0, 0, 0, time.UTC)}}}
	handler := operationsTestHandler(stub)
	request := httptest.NewRequest(http.MethodGet, "/api/meeting-sync-windows?from=2026-09-01T00:00:00Z&to=2026-09-30T00:00:00Z", nil)
	trustedHeaders(request, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"meetingId", "meetingKey", "collectionId", "name"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("redacted body leaked %s: %s", forbidden, response.Body.String())
		}
	}
}

func TestPrivateMeetingRoutesRequireAllowedServiceCaller(t *testing.T) {
	handler := operationsTestHandler(&operationsHandlerStub{})
	for _, test := range []struct {
		caller, token string
		want          int
	}{{"asset-api", "token", http.StatusOK}, {"unknown-api", "token", http.StatusUnauthorized}, {"asset-api", "wrong", http.StatusUnauthorized}} {
		request := httptest.NewRequest(http.MethodGet, "/priv/meeting-sync-windows?from=2026-09-01T00:00:00Z&to=2026-09-30T00:00:00Z", nil)
		request.Header.Set("Dapr-Caller-App-Id", test.caller)
		request.Header.Set("dapr-api-token", test.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("caller=%q status=%d body=%s", test.caller, response.Code, response.Body.String())
		}
	}
}

func TestPrivateMeetingSyncWindowsAcceptsOnlyConfiguredWorkloadIdentity(t *testing.T) {
	handler := operationsTestHandler(&operationsHandlerStub{},
		ServiceWorkloadAuth{TenantID: "tenant", Issuer: "https://sts.windows.net/tenant/", Audience: "api://meeting", ClientID: "warmer-client", ObjectID: "warmer-object", Caller: "asset-api"},
		ServiceWorkloadAuth{TenantID: "tenant", Issuer: "https://sts.windows.net/tenant/", Audience: "api://meeting", ClientID: "line-client", ObjectID: "line-object", Caller: "hhc-line-function-bot"},
	)
	for _, test := range []struct {
		path, clientID, objectID string
		want                     int
	}{
		{"/priv/meeting-sync-windows", "warmer-client", "warmer-object", http.StatusOK},
		{"/priv/meeting-sync-windows", "line-client", "line-object", http.StatusOK},
		{"/priv/meeting-sync-windows", "other-client", "warmer-object", http.StatusUnauthorized},
		{"/priv/meeting-sync-windows", "warmer-client", "other-object", http.StatusUnauthorized},
		{"/priv/meeting-occurrences", "warmer-client", "warmer-object", http.StatusUnauthorized},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path+"?from=2026-09-01T00:00:00Z&to=2026-09-30T00:00:00Z", nil)
		request.Header.Set("X-MS-CLIENT-PRINCIPAL", workloadPrincipal(t, test.clientID, test.objectID))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("path=%s client=%s object=%s status=%d body=%s", test.path, test.clientID, test.objectID, response.Code, response.Body.String())
		}
	}
}

func workloadPrincipal(t *testing.T, clientID, objectID string) string {
	t.Helper()
	claims := map[string]any{"auth_typ": "aad", "claims": []map[string]string{
		{"typ": "tid", "val": "tenant"}, {"typ": "iss", "val": "https://sts.windows.net/tenant/"}, {"typ": "aud", "val": "api://meeting"},
		{"typ": "appid", "val": clientID}, {"typ": "oid", "val": objectID},
	}}
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestAdminMeetingOperationsRequireScopeIdempotencyAndIfMatch(t *testing.T) {
	handler := operationsTestHandler(&operationsHandlerStub{})
	for _, test := range []struct {
		name, method, path, scopes, headers, body string
		want                                      int
	}{
		{name: "read scope", method: http.MethodGet, path: "/api/admin/operations/church-units", scopes: "cms:read", want: http.StatusOK},
		{name: "wrong read scope", method: http.MethodGet, path: "/api/admin/operations/church-units", scopes: "cms:write", want: http.StatusForbidden},
		{name: "create needs idempotency", method: http.MethodPost, path: "/api/admin/operations/church-units", scopes: "cms:write", body: `{"key":"main","name":"Main"}`, want: http.StatusBadRequest},
		{name: "create", method: http.MethodPost, path: "/api/admin/operations/church-units", scopes: "cms:write", headers: "Idempotency-Key", body: `{"key":"main","name":"Main"}`, want: http.StatusCreated},
		{name: "update needs if match", method: http.MethodPut, path: "/api/admin/operations/church-units/id", scopes: "cms:write", body: `{"key":"main","name":"Main"}`, want: http.StatusPreconditionRequired},
		{name: "unknown action", method: http.MethodPost, path: "/api/admin/operations/church-units/id/delete", scopes: "cms:write", want: http.StatusNotFound},
		{name: "wrong method", method: http.MethodDelete, path: "/api/admin/operations/church-units", scopes: "cms:write", want: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			trustedHeaders(request, test.scopes)
			request.Header.Set("X-HHC-Request-ID", "request-1")
			if test.headers == "Idempotency-Key" {
				request.Header.Set("Idempotency-Key", "create-1")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func operationsTestHandler(service operationsHTTPService, workload ...ServiceWorkloadAuth) http.Handler {
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "token", false).
		WithOperations(service, map[string]bool{"asset-api": true, "hhc-line-function-bot": true})
	if len(workload) > 0 {
		handler.WithOperationsWorkloadAuth(workload...)
	}
	return handler.Routes()
}

func trustedHeaders(request *http.Request, scopes string) {
	request.Header.Set("Dapr-Caller-App-Id", "api-gateway")
	request.Header.Set("dapr-api-token", "token")
	request.Header.Set("X-HHC-User-ID", "user-1")
	request.Header.Set("X-HHC-Auth-Provider", "account-api")
	request.Header.Set("X-HHC-Scopes", scopes)
}

type operationsHandlerStub struct {
	meetings        []operations.Meeting
	occurrences     []operations.Occurrence
	occurrenceQuery operations.OccurrenceQuery
	windows         []operations.MediaSyncWindow
}

func (s *operationsHandlerStub) ListChurchUnits(context.Context, bool) ([]operations.ChurchUnit, error) {
	return nil, nil
}
func (s *operationsHandlerStub) GetChurchUnit(context.Context, string) (operations.ChurchUnit, error) {
	return operations.ChurchUnit{}, nil
}
func (s *operationsHandlerStub) CreateChurchUnit(context.Context, operations.ChurchUnitInput, string, string, string) (operations.ChurchUnit, error) {
	return operations.ChurchUnit{}, nil
}
func (s *operationsHandlerStub) SaveChurchUnit(context.Context, string, int64, operations.ChurchUnitInput, string, string) (operations.ChurchUnit, error) {
	return operations.ChurchUnit{}, nil
}
func (s *operationsHandlerStub) SetChurchUnitStatus(context.Context, string, int64, operations.Status, string, string) (operations.ChurchUnit, error) {
	return operations.ChurchUnit{}, nil
}
func (s *operationsHandlerStub) ListResources(context.Context, bool) ([]operations.Resource, error) {
	return nil, nil
}
func (s *operationsHandlerStub) GetResource(context.Context, string) (operations.Resource, error) {
	return operations.Resource{}, nil
}
func (s *operationsHandlerStub) CreateResource(context.Context, operations.ResourceInput, string, string, string) (operations.Resource, error) {
	return operations.Resource{}, nil
}
func (s *operationsHandlerStub) SaveResource(context.Context, string, int64, operations.ResourceInput, string, string) (operations.Resource, error) {
	return operations.Resource{}, nil
}
func (s *operationsHandlerStub) SetResourceStatus(context.Context, string, int64, operations.Status, string, string) (operations.Resource, error) {
	return operations.Resource{}, nil
}
func (s *operationsHandlerStub) ListMeetings(context.Context, bool) ([]operations.Meeting, error) {
	return s.meetings, nil
}
func (s *operationsHandlerStub) GetMeeting(context.Context, string) (operations.Meeting, error) {
	return operations.Meeting{}, nil
}
func (s *operationsHandlerStub) CreateMeeting(context.Context, operations.MeetingInput, string, string, string) (operations.MeetingMutation, error) {
	return operations.MeetingMutation{}, nil
}
func (s *operationsHandlerStub) SaveMeeting(context.Context, string, int64, operations.MeetingInput, string, string) (operations.MeetingMutation, error) {
	return operations.MeetingMutation{}, nil
}
func (s *operationsHandlerStub) SetMeetingStatus(context.Context, string, int64, operations.Status, string, string) (operations.Meeting, error) {
	return operations.Meeting{}, nil
}
func (s *operationsHandlerStub) PutOverride(context.Context, string, int64, operations.OccurrenceOverrideInput, string, string) (operations.OccurrenceOverride, error) {
	return operations.OccurrenceOverride{}, nil
}
func (s *operationsHandlerStub) DeleteOverride(context.Context, string, int64, string, string, string) (operations.Meeting, error) {
	return operations.Meeting{}, nil
}
func (s *operationsHandlerStub) ListOverrides(context.Context, string) ([]operations.OccurrenceOverride, error) {
	return nil, nil
}
func (s *operationsHandlerStub) ReplaceMeetingBindings(context.Context, string, int64, []string, string, string) (operations.Meeting, error) {
	return operations.Meeting{}, nil
}
func (s *operationsHandlerStub) ListMeetingBindings(context.Context, string) ([]string, error) {
	return nil, nil
}
func (s *operationsHandlerStub) ListOccurrences(_ context.Context, query operations.OccurrenceQuery) ([]operations.Occurrence, error) {
	s.occurrenceQuery = query
	return s.occurrences, nil
}
func (s *operationsHandlerStub) ListMediaSyncWindows(context.Context, time.Time, time.Time) ([]operations.MediaSyncWindow, error) {
	return s.windows, nil
}
