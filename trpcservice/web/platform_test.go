package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

func TestPlatformAdminPublishChatPinAndRollback(t *testing.T) {
	server, repository, resolver, sessionService := newPlatformTestServer(t)
	handler := server.Handler()

	requireStatus(t, handler, http.MethodPost, "/admin/v1/tenants", `{
		"id":"tenant-a","slug":"tenant-a","name":"Tenant A"
	}`, nil, http.StatusCreated)
	requireStatus(t, handler, http.MethodPost, "/admin/v1/tenants/tenant-a/apps", `{
		"id":"assistant","name":"Assistant"
	}`, nil, http.StatusCreated)
	createRevisionThroughAPI(t, handler, "tenant-a", "assistant", "revision-1", 1, "echo-v1")
	publishRevisionThroughAPI(t, handler, "tenant-a", "assistant", "revision-1")

	chatHeaders := map[string]string{
		HeaderTenantID:   "tenant-a",
		HeaderAgentAppID: "assistant",
		"X-Session-ID":   "conversation-1",
	}
	first := requireStatus(t, handler, http.MethodPost, "/v1/chat/completions", `{
		"model":"echo-v1","user":"user-1","messages":[{"role":"user","content":"first"}]
	}`, chatHeaders, http.StatusOK)
	require.Contains(t, first.Body.String(), `"model":"echo-v1"`)
	require.Contains(t, first.Body.String(), "echo: first")

	createRevisionThroughAPI(t, handler, "tenant-a", "assistant", "revision-2", 2, "echo-v2")
	publishRevisionThroughAPI(t, handler, "tenant-a", "assistant", "revision-2")
	second := requireStatus(t, handler, http.MethodPost, "/v1/chat/completions", `{
		"model":"echo-v2","user":"user-1","messages":[{"role":"user","content":"second"}]
	}`, chatHeaders, http.StatusOK)
	require.Contains(t, second.Body.String(), `"model":"echo-v2"`)

	pinnedHeaders := cloneHeaders(chatHeaders)
	pinnedHeaders[HeaderAgentRevisionID] = "revision-1"
	pinned := requireStatus(t, handler, http.MethodPost, "/v1/chat/completions", `{
		"model":"echo-v1","user":"user-1","messages":[{"role":"user","content":"pinned"}]
	}`, pinnedHeaders, http.StatusOK)
	require.Contains(t, pinned.Body.String(), `"model":"echo-v1"`)

	rollback := publishRevisionThroughAPI(t, handler, "tenant-a", "assistant", "revision-1")
	var rollbackResponse publishRevisionResponse
	require.NoError(t, json.Unmarshal(rollback.Body.Bytes(), &rollbackResponse))
	require.Equal(t, "revision-1", rollbackResponse.App.RoutingPolicy.DefaultRevisionID)
	require.Equal(t, uint64(3), rollbackResponse.App.RoutingVersion)

	afterRollback := requireStatus(t, handler, http.MethodPost, "/v1/chat/completions", `{
		"model":"echo-v1","user":"user-1","messages":[{"role":"user","content":"rollback"}]
	}`, chatHeaders, http.StatusOK)
	require.Contains(t, afterRollback.Body.String(), `"model":"echo-v1"`)
	require.Equal(t, 2, resolver.CacheSize())

	storedSession, err := sessionService.GetSession(context.Background(), session.Key{
		AppName: "t/tenant-a/a/assistant", UserID: "user-1", SessionID: "conversation-1",
	})
	require.NoError(t, err)
	require.NotNil(t, storedSession)
	require.GreaterOrEqual(t, storedSession.GetEventCount(), 8)

	resolved, err := repository.ResolveRevision(
		context.Background(), tenant.TenantContext{TenantID: "tenant-a"}, "assistant", "",
	)
	require.NoError(t, err)
	require.Equal(t, "revision-1", resolved.ID)
}

func TestPlatformTenantIsolationAndErrors(t *testing.T) {
	server, _, resolver, _ := newPlatformTestServer(t)
	handler := server.Handler()
	for _, tenantID := range []string{"tenant-a", "tenant-b"} {
		requireStatus(t, handler, http.MethodPost, "/admin/v1/tenants", fmt.Sprintf(`{
			"id":%q,"slug":%q,"name":%q
		}`, tenantID, tenantID, "Tenant "+tenantID), nil, http.StatusCreated)
		requireStatus(
			t, handler, http.MethodPost, "/admin/v1/tenants/"+tenantID+"/apps", `{
				"id":"assistant","name":"Assistant"
			}`, nil, http.StatusCreated,
		)
		modelName := "model-" + tenantID
		createRevisionThroughAPI(t, handler, tenantID, "assistant", "revision-1", 1, modelName)
		publishRevisionThroughAPI(t, handler, tenantID, "assistant", "revision-1")
	}

	for _, tenantID := range []string{"tenant-a", "tenant-b"} {
		modelName := "model-" + tenantID
		response := requireStatus(t, handler, http.MethodPost, "/v1/chat/completions", `{
			"model":"ignored","messages":[{"role":"user","content":"isolated"}]
		}`, map[string]string{
			HeaderTenantID: tenantID, HeaderAgentAppID: "assistant",
		}, http.StatusOK)
		require.Contains(t, response.Body.String(), `"model":"`+modelName+`"`)
	}
	require.Equal(t, 2, resolver.CacheSize())

	missingRoute := requireStatus(
		t, handler, http.MethodPost, "/v1/chat/completions",
		`{"messages":[{"role":"user","content":"missing"}]}`,
		nil, http.StatusBadRequest,
	)
	require.Contains(t, missingRoute.Body.String(), `"code":"missing_route"`)

	unknownRevision := requireStatus(
		t, handler, http.MethodGet,
		"/admin/v1/tenants/tenant-b/apps/assistant/revisions/private-a",
		"", nil, http.StatusNotFound,
	)
	require.JSONEq(t, `{
		"error":{"code":"not_found","message":"resource not found"}
	}`, unknownRevision.Body.String())

	invalidJSON := requireStatus(
		t, handler, http.MethodPost, "/admin/v1/tenants",
		`{"id":"tenant-c","slug":"tenant-c","name":"Tenant C","unknown":true}`,
		nil, http.StatusBadRequest,
	)
	require.Contains(t, invalidJSON.Body.String(), `"code":"invalid_json"`)

	duplicate := requireStatus(t, handler, http.MethodPost, "/admin/v1/tenants", `{
		"id":"tenant-a","slug":"tenant-a","name":"Duplicate"
	}`, nil, http.StatusConflict)
	require.Contains(t, duplicate.Body.String(), `"code":"already_exists"`)
}

func TestPlatformDynamicStreaming(t *testing.T) {
	server, _, _, _ := newPlatformTestServer(t)
	handler := server.Handler()
	requireStatus(t, handler, http.MethodPost, "/admin/v1/tenants", `{
		"id":"tenant-a","slug":"tenant-a","name":"Tenant A"
	}`, nil, http.StatusCreated)
	requireStatus(t, handler, http.MethodPost, "/admin/v1/tenants/tenant-a/apps", `{
		"id":"assistant","name":"Assistant"
	}`, nil, http.StatusCreated)
	createRevisionThroughAPI(t, handler, "tenant-a", "assistant", "revision-1", 1, "echo-v1")
	publishRevisionThroughAPI(t, handler, "tenant-a", "assistant", "revision-1")

	response := requireStatus(t, handler, http.MethodPost, "/v1/chat/completions", `{
		"model":"echo-v1","stream":true,"messages":[{"role":"user","content":"stream"}]
	}`, map[string]string{
		HeaderTenantID: "tenant-a", HeaderAgentAppID: "assistant",
	}, http.StatusOK)
	require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	require.Contains(t, response.Body.String(), "echo: ")
	require.Contains(t, response.Body.String(), "stream")
	require.Contains(t, response.Body.String(), "data: [DONE]")
}

func TestPlatformHTTPMethodAndCORS(t *testing.T) {
	server, _, _, _ := newPlatformTestServer(t)
	handler := server.Handler()

	wrongMethod := requireStatus(
		t, handler, http.MethodGet, "/admin/v1/tenants", "", nil,
		http.StatusMethodNotAllowed,
	)
	require.Equal(t, http.MethodPost, wrongMethod.Header().Get("Allow"))

	preflight := requireStatus(
		t, handler, http.MethodOptions, "/v1/chat/completions", "", nil,
		http.StatusNoContent,
	)
	require.Contains(t, preflight.Header().Get("Access-Control-Allow-Headers"), HeaderTenantID)
	require.Equal(t, http.MethodPost, preflight.Header().Get("Access-Control-Allow-Methods"))
}

func newPlatformTestServer(
	t *testing.T,
) (*PlatformServer, *tenant.MemoryRepository, *platformagent.RuntimeResolver, session.Service) {
	t.Helper()
	repository := tenant.NewMemoryRepository()
	sessionService := sessioninmemory.NewSessionService()
	t.Cleanup(func() { require.NoError(t, sessionService.Close()) })
	resolver, err := platformagent.NewRuntimeResolver(
		repository,
		func(
			_ context.Context,
			revision tenant.AgentRevision,
		) (*platformagent.Runtime, error) {
			return platformagent.NewRuntimeFromRevision(revision, sessionService)
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resolver.Close()) })
	server, err := NewPlatformServer(repository, resolver)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	return server, repository, resolver, sessionService
}

func createRevisionThroughAPI(
	t *testing.T,
	handler http.Handler,
	tenantID string,
	appID string,
	revisionID string,
	revisionNo uint64,
	modelName string,
) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{
		"id":%q,
		"revision_no":%d,
		"created_by":"test-admin",
		"config":{
			"agent_name":"test-agent",
			"instruction":"Answer through the deterministic model.",
			"model":{"provider":"deterministic","name":%q}
		}
	}`, revisionID, revisionNo, modelName)
	return requireStatus(
		t,
		handler,
		http.MethodPost,
		fmt.Sprintf("/admin/v1/tenants/%s/apps/%s/revisions", tenantID, appID),
		body,
		nil,
		http.StatusCreated,
	)
}

func publishRevisionThroughAPI(
	t *testing.T,
	handler http.Handler,
	tenantID string,
	appID string,
	revisionID string,
) *httptest.ResponseRecorder {
	t.Helper()
	return requireStatus(
		t,
		handler,
		http.MethodPost,
		fmt.Sprintf(
			"/admin/v1/tenants/%s/apps/%s/revisions/%s/publish",
			tenantID,
			appID,
			revisionID,
		),
		"",
		nil,
		http.StatusOK,
	)
}

func requireStatus(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body string,
	headers map[string]string,
	expectedStatus int,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, expectedStatus, response.Code, response.Body.String())
	return response
}

func cloneHeaders(headers map[string]string) map[string]string {
	cloned := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		cloned[name] = value
	}
	return cloned
}
