package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const (
	keyTenantA   = "chat-key-tenant-a-0123456789"
	keyTenantB   = "chat-key-tenant-b-0123456789"
	keyUnknown   = "chat-key-unknown-0123456789"
	principalA   = "principal-a"
	principalB   = "principal-b"
	appAssistant = "assistant"
)

// The pin makes a published revision invisible to sessions that already
// started, and a rollback leaves those sessions alone as well.
func TestPlatformPinsSessionToFirstRevision(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")

	headers := chatHeaders(keyTenantA, appAssistant)
	headers[HeaderSessionID] = "conversation-1"
	first := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"first"}]
	}`, headers, http.StatusOK)
	require.Contains(t, first.Body.String(), `"model":"echo-v1"`)
	require.Contains(t, first.Body.String(), "echo: first")
	require.Equal(t, "revision-1", first.Header().Get(HeaderAgentRevisionID))
	require.Equal(t, "conversation-1", first.Header().Get(HeaderSessionID))

	createRevisionThroughAPI(t, platform.handler, "tenant-a", appAssistant, "revision-2", 2, "echo-v2")
	publishRevisionThroughAPI(t, platform.handler, "tenant-a", appAssistant, "revision-2")

	// The pinned session keeps serving revision-1 even though revision-2 is now
	// the default route.
	pinned := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"second"}]
	}`, headers, http.StatusOK)
	require.Contains(t, pinned.Body.String(), `"model":"echo-v1"`)
	require.Equal(t, "revision-1", pinned.Header().Get(HeaderAgentRevisionID))

	// A new session takes the current default.
	fresh := cloneHeaders(headers)
	fresh[HeaderSessionID] = "conversation-2"
	current := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"fresh"}]
	}`, fresh, http.StatusOK)
	require.Contains(t, current.Body.String(), `"model":"echo-v2"`)
	require.Equal(t, "revision-2", current.Header().Get(HeaderAgentRevisionID))

	// Rolling back does not move either pin.
	rollback := publishRevisionThroughAPI(t, platform.handler, "tenant-a", appAssistant, "revision-1")
	var rollbackResponse publishRevisionResponse
	require.NoError(t, json.Unmarshal(rollback.Body.Bytes(), &rollbackResponse))
	require.Equal(t, "revision-1", rollbackResponse.App.RoutingPolicy.DefaultRevisionID)
	require.Equal(t, uint64(3), rollbackResponse.App.RoutingVersion)

	afterRollback := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"rollback"}]
	}`, fresh, http.StatusOK)
	require.Contains(t, afterRollback.Body.String(), `"model":"echo-v2"`)
	require.Equal(t, "revision-2", afterRollback.Header().Get(HeaderAgentRevisionID))
	require.Equal(t, 2, platform.directory.Size())
	require.Equal(t, 2, platform.resolver.CacheSize())

	stored, err := platform.sessions.GetSession(context.Background(), session.Key{
		AppName:   "t/tenant-a/a/" + appAssistant,
		UserID:    "u/" + principalA,
		SessionID: "conversation-1",
	})
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.GreaterOrEqual(t, stored.GetEventCount(), 4)
}

// X-Agent-Revision-ID selects the revision of a first run only. On a pinned
// session it may confirm the pin, never change it.
func TestPlatformRevisionHintCannotMovePinnedSession(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")
	createRevisionThroughAPI(t, platform.handler, "tenant-a", appAssistant, "revision-2", 2, "echo-v2")
	publishRevisionThroughAPI(t, platform.handler, "tenant-a", appAssistant, "revision-2")

	// A hint on a fresh session picks the older revision instead of the default.
	headers := chatHeaders(keyTenantA, appAssistant)
	headers[HeaderSessionID] = "conversation-1"
	headers[HeaderAgentRevisionID] = "revision-1"
	hinted := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"hinted"}]
	}`, headers, http.StatusOK)
	require.Contains(t, hinted.Body.String(), `"model":"echo-v1"`)
	require.Equal(t, "revision-1", hinted.Header().Get(HeaderAgentRevisionID))

	// Repeating the same hint is consistent with the pin, so it is allowed.
	repeated := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"repeated"}]
	}`, headers, http.StatusOK)
	require.Contains(t, repeated.Body.String(), `"model":"echo-v1"`)

	// A different hint is a conflict, not a silent switch.
	conflicting := cloneHeaders(headers)
	conflicting[HeaderAgentRevisionID] = "revision-2"
	conflict := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"conflicting"}]
	}`, conflicting, http.StatusConflict)
	require.Contains(t, conflict.Body.String(), `"code":"pin_conflict"`)
	require.Contains(t, conflict.Body.String(), "revision-1")

	// Dropping the hint keeps serving the pin, not the default revision.
	unhinted := cloneHeaders(headers)
	delete(unhinted, HeaderAgentRevisionID)
	stillPinned := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"still pinned"}]
	}`, unhinted, http.StatusOK)
	require.Contains(t, stillPinned.Body.String(), `"model":"echo-v1"`)

	malformed := cloneHeaders(headers)
	malformed[HeaderAgentRevisionID] = "revision 1"
	invalid := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"invalid"}]
	}`, malformed, http.StatusBadRequest)
	require.Contains(t, invalid.Body.String(), `"code":"invalid_argument"`)
}

// The conversation identity comes from the credential. A request body that
// claims another user must not reach the Session key space.
func TestPlatformChatIgnoresRequestSuppliedUser(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")

	headers := chatHeaders(keyTenantA, appAssistant)
	headers[HeaderSessionID] = "conversation-1"
	for _, content := range []string{"first", "second"} {
		requireStatus(t, platform.handler, http.MethodPost, chatPath, fmt.Sprintf(`{
			"model":"ignored","user":"attacker","messages":[{"role":"user","content":%q}]
		}`, content), headers, http.StatusOK)
	}

	appName := "t/tenant-a/a/" + appAssistant
	trusted, err := platform.sessions.GetSession(context.Background(), session.Key{
		AppName: appName, UserID: "u/" + principalA, SessionID: "conversation-1",
	})
	require.NoError(t, err)
	require.NotNil(t, trusted)
	require.GreaterOrEqual(t, trusted.GetEventCount(), 4)

	for _, claimed := range []string{"attacker", "u/attacker"} {
		spoofed, sessionErr := platform.sessions.GetSession(context.Background(), session.Key{
			AppName: appName, UserID: claimed, SessionID: "conversation-1",
		})
		require.NoError(t, sessionErr)
		require.Nil(t, spoofed, "request body user %q reached the session store", claimed)
	}
}

func TestPlatformChatAuthenticationMatrix(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")
	body := `{"model":"ignored","messages":[{"role":"user","content":"hello"}]}`

	unauthenticated := map[string]map[string]string{
		"missing credential": {HeaderAgentAppID: appAssistant},
		"unknown key": {
			HeaderAuthorization: "Bearer " + keyUnknown,
			HeaderAgentAppID:    appAssistant,
		},
		"wrong scheme": {
			HeaderAuthorization: "Basic " + keyTenantA,
			HeaderAgentAppID:    appAssistant,
		},
		"empty token": {
			HeaderAuthorization: "Bearer ",
			HeaderAgentAppID:    appAssistant,
		},
		"raw key": {
			HeaderAuthorization: keyTenantA,
			HeaderAgentAppID:    appAssistant,
		},
	}
	for name, headers := range unauthenticated {
		t.Run(name, func(t *testing.T) {
			response := requireStatus(
				t, platform.handler, http.MethodPost, chatPath, body, headers,
				http.StatusUnauthorized,
			)
			require.Contains(t, response.Body.String(), `"code":"unauthenticated"`)
			require.Equal(t, "Bearer", response.Header().Get("WWW-Authenticate"))
		})
	}

	forbidden := map[string]map[string]string{
		"tenant assertion mismatch": {
			HeaderAuthorization: "Bearer " + keyTenantA,
			HeaderTenantID:      "tenant-b",
			HeaderAgentAppID:    appAssistant,
		},
		"app outside the grant": {
			HeaderAuthorization: "Bearer " + keyTenantA,
			HeaderAgentAppID:    "reporter",
		},
	}
	for name, headers := range forbidden {
		t.Run(name, func(t *testing.T) {
			response := requireStatus(
				t, platform.handler, http.MethodPost, chatPath, body, headers,
				http.StatusForbidden,
			)
			require.Contains(t, response.Body.String(), `"code":"forbidden"`)
		})
	}

	// A matching assertion is redundant but accepted.
	asserted := chatHeaders(keyTenantA, appAssistant)
	asserted[HeaderTenantID] = "tenant-a"
	requireStatus(t, platform.handler, http.MethodPost, chatPath, body, asserted, http.StatusOK)

	missingApp := requireStatus(
		t, platform.handler, http.MethodPost, chatPath, body,
		map[string]string{HeaderAuthorization: "Bearer " + keyTenantA},
		http.StatusBadRequest,
	)
	require.Contains(t, missingApp.Body.String(), `"code":"missing_route"`)

	// A browser cannot attach a credential to a preflight request.
	preflight := requireStatus(
		t, platform.handler, http.MethodOptions, chatPath, "", nil, http.StatusNoContent,
	)
	require.Contains(t, preflight.Header().Get("Access-Control-Allow-Headers"), HeaderAuthorization)
}

// A client that lets the platform mint the session id has to get it back, or it
// cannot continue the conversation.
func TestPlatformReturnsGeneratedSessionID(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")
	headers := chatHeaders(keyTenantA, appAssistant)

	first := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"first"}]
	}`, headers, http.StatusOK)
	generated := first.Header().Get(HeaderSessionID)
	require.NoError(t, tenant.ValidateResourceID("session id", generated))
	require.Equal(t, "revision-1", first.Header().Get(HeaderAgentRevisionID))
	require.Contains(
		t, first.Header().Get("Access-Control-Expose-Headers"), HeaderSessionID,
	)

	second := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"second"}]
	}`, headers, http.StatusOK)
	require.NotEqual(t, generated, second.Header().Get(HeaderSessionID))

	// Reusing the returned id continues the same conversation.
	resumed := cloneHeaders(headers)
	resumed[HeaderSessionID] = generated
	requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"resumed"}]
	}`, resumed, http.StatusOK)

	stored, err := platform.sessions.GetSession(context.Background(), session.Key{
		AppName:   "t/tenant-a/a/" + appAssistant,
		UserID:    "u/" + principalA,
		SessionID: generated,
	})
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.GreaterOrEqual(t, stored.GetEventCount(), 4)

	streamed := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","stream":true,"messages":[{"role":"user","content":"stream"}]
	}`, headers, http.StatusOK)
	require.Equal(t, "text/event-stream", streamed.Header().Get("Content-Type"))
	require.NoError(t, tenant.ValidateResourceID(
		"session id", streamed.Header().Get(HeaderSessionID),
	))
	require.Equal(t, "revision-1", streamed.Header().Get(HeaderAgentRevisionID))
}

func TestPlatformRejectsInvalidSessionID(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")

	for _, sessionID := range []string{"conversation 1", "conversation/1", "-leading-dash"} {
		headers := chatHeaders(keyTenantA, appAssistant)
		headers[HeaderSessionID] = sessionID
		response := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
			"model":"ignored","messages":[{"role":"user","content":"hello"}]
		}`, headers, http.StatusBadRequest)
		require.Contains(t, response.Body.String(), `"code":"invalid_session_id"`)
	}
	require.Zero(t, platform.directory.Size())
}

// The adapter owns the status line, so the platform has to publish the session
// and revision headers before handing the request over. A body the adapter
// rejects still pins the session: that is the current, documented semantics.
func TestPlatformPublishesHeadersBeforeAdapterErrors(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")

	response := doChat(t, platform.handler, `{"model":`, chatHeaders(keyTenantA, appAssistant))
	require.GreaterOrEqual(t, response.Code, http.StatusBadRequest, response.Body.String())
	require.NoError(t, tenant.ValidateResourceID(
		"session id", response.Header().Get(HeaderSessionID),
	))
	require.Equal(t, "revision-1", response.Header().Get(HeaderAgentRevisionID))
	require.Equal(t, 1, platform.directory.Size())
}

// Two first runs of one session can resolve different candidates before either
// reaches the directory. Both must end up on the single winner, and the loser
// has to hand its runtime lease back.
func TestPlatformConcurrentFirstRunsAgreeOnOneRevision(t *testing.T) {
	barrier := &barrierDirectory{
		inner:   sessiondir.NewMemoryDirectory(),
		arrived: make(chan struct{}),
		release: make(chan struct{}),
	}
	platform := newPlatformTestServerWithDirectory(t, barrier)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")
	createRevisionThroughAPI(t, platform.handler, "tenant-a", appAssistant, "revision-2", 2, "echo-v2")
	publishRevisionThroughAPI(t, platform.handler, "tenant-a", appAssistant, "revision-2")

	// One request pins through the development hint, the other through the
	// current default route.
	hinted := chatHeaders(keyTenantA, appAssistant)
	hinted[HeaderSessionID] = "conversation-1"
	hinted[HeaderAgentRevisionID] = "revision-1"
	defaulted := chatHeaders(keyTenantA, appAssistant)
	defaulted[HeaderSessionID] = "conversation-1"

	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, headers := range []map[string]string{hinted, defaulted} {
		go func(headers map[string]string) {
			responses <- doChat(t, platform.handler, `{
				"model":"ignored","messages":[{"role":"user","content":"concurrent"}]
			}`, headers)
		}(headers)
	}
	// Both requests are now holding a resolved candidate and are parked in
	// EnsurePin, so neither could have seen the other's pin.
	barrier.awaitArrival(t)
	barrier.awaitArrival(t)
	barrier.releaseAll()

	first := <-responses
	second := <-responses
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())

	winner := first.Header().Get(HeaderAgentRevisionID)
	require.Contains(t, []string{"revision-1", "revision-2"}, winner)
	require.Equal(t, winner, second.Header().Get(HeaderAgentRevisionID))
	winnerModel := map[string]string{"revision-1": "echo-v1", "revision-2": "echo-v2"}[winner]
	require.Contains(t, first.Body.String(), `"model":"`+winnerModel+`"`)
	require.Contains(t, second.Body.String(), `"model":"`+winnerModel+`"`)

	pinned, found, err := barrier.inner.GetPin(context.Background(), sessiondir.Key{
		TenantID:    "tenant-a",
		AppID:       appAssistant,
		PrincipalID: principalA,
		SessionID:   "conversation-1",
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, winner, pinned)

	// Close only returns once every lease is back, so a loser that kept its
	// lease would hang here instead of reporting a leak.
	closed := make(chan error, 1)
	go func() { closed <- platform.resolver.Close() }()
	select {
	case closeErr := <-closed:
		require.NoError(t, closeErr)
	case <-time.After(10 * time.Second):
		t.Fatal("resolver did not close: a runtime lease leaked")
	}
}

func TestPlatformTenantIsolationAndErrors(t *testing.T) {
	platform := newPlatformTestServer(t)
	for _, tenantID := range []string{"tenant-a", "tenant-b"} {
		seedTenantAppRevision(
			t, platform.handler, tenantID, appAssistant, "revision-1", 1, "model-"+tenantID,
		)
	}

	// Each credential reaches its own tenant only; the tenant is never taken
	// from a request header.
	for apiKey, tenantID := range map[string]string{
		keyTenantA: "tenant-a",
		keyTenantB: "tenant-b",
	} {
		headers := chatHeaders(apiKey, appAssistant)
		headers[HeaderSessionID] = "conversation-shared"
		response := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
			"model":"ignored","messages":[{"role":"user","content":"isolated"}]
		}`, headers, http.StatusOK)
		require.Contains(t, response.Body.String(), `"model":"model-`+tenantID+`"`)
	}
	require.Equal(t, 2, platform.resolver.CacheSize())
	// The same session id under two tenants is two conversations.
	require.Equal(t, 2, platform.directory.Size())

	unknownRevision := requireStatus(
		t, platform.handler, http.MethodGet,
		"/admin/v1/tenants/tenant-b/apps/assistant/revisions/private-a",
		"", nil, http.StatusNotFound,
	)
	require.JSONEq(t, `{
		"error":{"code":"not_found","message":"resource not found"}
	}`, unknownRevision.Body.String())

	invalidJSON := requireStatus(
		t, platform.handler, http.MethodPost, "/admin/v1/tenants",
		`{"id":"tenant-c","slug":"tenant-c","name":"Tenant C","unknown":true}`,
		nil, http.StatusBadRequest,
	)
	require.Contains(t, invalidJSON.Body.String(), `"code":"invalid_json"`)

	duplicate := requireStatus(t, platform.handler, http.MethodPost, "/admin/v1/tenants", `{
		"id":"tenant-a","slug":"tenant-a","name":"Duplicate"
	}`, nil, http.StatusConflict)
	require.Contains(t, duplicate.Body.String(), `"code":"already_exists"`)
}

func TestPlatformDynamicStreaming(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")

	response := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","stream":true,"messages":[{"role":"user","content":"stream"}]
	}`, chatHeaders(keyTenantA, appAssistant), http.StatusOK)
	require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	require.Contains(t, response.Body.String(), "echo: ")
	require.Contains(t, response.Body.String(), "stream")
	require.Contains(t, response.Body.String(), "data: [DONE]")
}

// The runtime lease must cover the whole SSE handler, otherwise the resolver
// could close the Runner and its adapter while chunks are still being written.
func TestPlatformStreamingHoldsRuntimeLeaseUntilHandlerReturns(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")

	writer := newBlockingResponseWriter()
	request := httptest.NewRequest(http.MethodPost, chatPath, bytes.NewBufferString(`{
		"model":"ignored","stream":true,"messages":[{"role":"user","content":"stream"}]
	}`))
	request.Header.Set("Content-Type", "application/json")
	for name, value := range chatHeaders(keyTenantA, appAssistant) {
		request.Header.Set(name, value)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		platform.handler.ServeHTTP(writer, request)
	}()
	// Every failure below must still unblock the handler: while it is parked in
	// Write it holds a lease, and the resolver.Close from the server cleanup
	// would wait for that lease forever instead of reporting the failure.
	t.Cleanup(func() {
		writer.release()
		<-served
	})

	select {
	case <-writer.started:
	case <-served:
		t.Fatalf("handler returned without streaming: %s", writer.bodyString())
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- platform.resolver.Close() }()
	scope := tenant.TenantContext{TenantID: "tenant-a"}
	require.Eventually(t, func() bool {
		// Close marks the resolver asynchronously, so a probe can still win the
		// race and take a real lease. Returning it keeps closeAll from waiting on
		// a lease this test would otherwise never release.
		resolved, resolveErr := platform.resolver.Resolve(
			context.Background(), scope, appAssistant, "",
		)
		if resolveErr == nil {
			resolved.Release()
			return false
		}
		return errors.Is(resolveErr, platformagent.ErrResolverClosed)
	}, time.Second, time.Millisecond)
	select {
	case <-closeResult:
		t.Fatal("resolver closed while a streaming handler still held the lease")
	default:
	}

	writer.release()
	<-served
	require.NoError(t, <-closeResult)
	require.Equal(t, "text/event-stream", writer.Header().Get("Content-Type"))
	require.NotEmpty(t, writer.Header().Get(HeaderSessionID))
	require.Contains(t, writer.bodyString(), "data: [DONE]")

	// The platform keeps no runtime state of its own, so traffic stops as soon
	// as the resolver is closed.
	unavailable := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"after close"}]
	}`, chatHeaders(keyTenantA, appAssistant), http.StatusServiceUnavailable)
	require.Contains(t, unavailable.Body.String(), `"code":"runtime_unavailable"`)
}

func TestPlatformRejectsRuntimeWithoutOpenAIHandler(t *testing.T) {
	repository := tenant.NewMemoryRepository()
	server, err := NewPlatformServer(
		repository,
		resolverFunc(func(
			context.Context,
			tenant.TenantContext,
			string,
			string,
		) (platformagent.ResolvedRuntime, error) {
			return platformagent.ResolvedRuntime{
				Runtime: &platformagent.Runtime{},
				Revision: tenant.AgentRevision{
					ID: "revision-1", TenantID: "tenant-a", AgentAppID: appAssistant,
				},
			}, nil
		}),
		newTestAuthenticator(t),
		sessiondir.NewMemoryDirectory(),
	)
	require.NoError(t, err)

	response := requireStatus(t, server.Handler(), http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"broken"}]
	}`, chatHeaders(keyTenantA, appAssistant), http.StatusInternalServerError)
	require.Contains(t, response.Body.String(), `"code":"internal_error"`)
}

func TestNewPlatformServerRequiresEveryDependency(t *testing.T) {
	repository := tenant.NewMemoryRepository()
	resolver := resolverFunc(func(
		context.Context,
		tenant.TenantContext,
		string,
		string,
	) (platformagent.ResolvedRuntime, error) {
		return platformagent.ResolvedRuntime{}, nil
	})
	authenticator := newTestAuthenticator(t)
	directory := sessiondir.NewMemoryDirectory()

	_, err := NewPlatformServer(nil, resolver, authenticator, directory)
	require.ErrorContains(t, err, "tenant repository is required")
	_, err = NewPlatformServer(repository, nil, authenticator, directory)
	require.ErrorContains(t, err, "runtime resolver is required")
	_, err = NewPlatformServer(repository, resolver, nil, directory)
	require.ErrorContains(t, err, "chat authenticator is required")
	_, err = NewPlatformServer(repository, resolver, authenticator, nil)
	require.ErrorContains(t, err, "session directory is required")

	server, err := NewPlatformServer(repository, resolver, authenticator, directory)
	require.NoError(t, err)
	require.NotNil(t, server.Handler())
}

func TestPlatformHTTPMethodAndCORS(t *testing.T) {
	platform := newPlatformTestServer(t)
	health := requireStatus(t, platform.handler, http.MethodGet, "/healthz", "", nil, http.StatusOK)
	require.JSONEq(t, `{"status":"ok"}`, health.Body.String())

	wrongMethod := requireStatus(
		t, platform.handler, http.MethodGet, "/admin/v1/tenants", "", nil,
		http.StatusMethodNotAllowed,
	)
	require.Equal(t, http.MethodPost, wrongMethod.Header().Get("Allow"))

	chatMethod := requireStatus(
		t, platform.handler, http.MethodGet, chatPath, "", nil, http.StatusMethodNotAllowed,
	)
	require.Equal(t, "POST, OPTIONS", chatMethod.Header().Get("Allow"))
	require.Equal(t, "*", chatMethod.Header().Get("Access-Control-Allow-Origin"))

	preflight := requireStatus(
		t, platform.handler, http.MethodOptions, chatPath, "", nil, http.StatusNoContent,
	)
	require.Equal(t, "*", preflight.Header().Get("Access-Control-Allow-Origin"))
	allowed := preflight.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{
		HeaderAuthorization, HeaderTenantID, HeaderAgentAppID, HeaderAgentRevisionID, HeaderSessionID,
	} {
		require.Contains(t, allowed, header)
	}
	exposed := preflight.Header().Get("Access-Control-Expose-Headers")
	require.Contains(t, exposed, HeaderSessionID)
	require.Contains(t, exposed, HeaderAgentRevisionID)
	require.Equal(t, http.MethodPost, preflight.Header().Get("Access-Control-Allow-Methods"))
}

// A browser reads an error body only when the actual response carries the CORS
// headers. The refusals below all return before routing, which is exactly where
// they used to be dropped, leaving the caller with an opaque CORS failure
// instead of the documented JSON error.
func TestPlatformPublishesCORSHeadersOnEarlyRefusals(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")
	body := `{"model":"ignored","messages":[{"role":"user","content":"hello"}]}`

	pinned := chatHeaders(keyTenantA, appAssistant)
	pinned[HeaderSessionID] = "conversation-1"
	requireStatus(t, platform.handler, http.MethodPost, chatPath, body, pinned, http.StatusOK)
	conflicting := cloneHeaders(pinned)
	conflicting[HeaderAgentRevisionID] = "revision-2"

	refusals := []struct {
		name           string
		headers        map[string]string
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "unauthenticated",
			headers:        map[string]string{HeaderAgentAppID: appAssistant},
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   "unauthenticated",
		},
		{
			name: "forbidden app",
			headers: map[string]string{
				HeaderAuthorization: "Bearer " + keyTenantA,
				HeaderAgentAppID:    "reporter",
			},
			expectedStatus: http.StatusForbidden,
			expectedCode:   "forbidden",
		},
		{
			name:           "missing route",
			headers:        map[string]string{HeaderAuthorization: "Bearer " + keyTenantA},
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "missing_route",
		},
		{
			name:           "pin conflict",
			headers:        conflicting,
			expectedStatus: http.StatusConflict,
			expectedCode:   "pin_conflict",
		},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			response := requireStatus(
				t, platform.handler, http.MethodPost, chatPath, body, refusal.headers,
				refusal.expectedStatus,
			)
			require.Contains(t, response.Body.String(), `"code":"`+refusal.expectedCode+`"`)
			require.Equal(t, "*", response.Header().Get("Access-Control-Allow-Origin"))
			exposed := response.Header().Get("Access-Control-Expose-Headers")
			require.Contains(t, exposed, HeaderSessionID)
			require.Contains(t, exposed, HeaderAgentRevisionID)
		})
	}

	// A refusal names no revision, so the run scope headers stay absent: the
	// session was never pinned and there is nothing to report.
	unauthenticated := requireStatus(
		t, platform.handler, http.MethodPost, chatPath, body,
		map[string]string{HeaderAgentAppID: appAssistant}, http.StatusUnauthorized,
	)
	require.Empty(t, unauthenticated.Header().Get(HeaderAgentRevisionID))
	require.Empty(t, unauthenticated.Header().Get(HeaderSessionID))
}

const chatPath = "/v1/chat/completions"

type platformTestServer struct {
	handler   http.Handler
	resolver  *platformagent.RuntimeResolver
	sessions  session.Service
	directory *sessiondir.MemoryDirectory
}

func newPlatformTestServer(t *testing.T) *platformTestServer {
	t.Helper()
	return newPlatformTestServerWithDirectory(t, nil)
}

// newPlatformTestServerWithDirectory lets a test observe or delay the pin
// without changing how the platform is wired.
func newPlatformTestServerWithDirectory(
	t *testing.T,
	wrapper *barrierDirectory,
) *platformTestServer {
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

	directory := sessiondir.NewMemoryDirectory()
	var chatDirectory sessiondir.Directory = directory
	if wrapper != nil {
		directory = wrapper.inner
		chatDirectory = wrapper
		// Runs before the resolver is closed, so a failed test never leaves a
		// request parked at the barrier while Close waits for its lease.
		t.Cleanup(wrapper.releaseAll)
	}
	server, err := NewPlatformServer(
		repository, resolver, newTestAuthenticator(t), chatDirectory,
	)
	require.NoError(t, err)
	return &platformTestServer{
		handler:   server.Handler(),
		resolver:  resolver,
		sessions:  sessionService,
		directory: directory,
	}
}

func newTestAuthenticator(t *testing.T) identity.Authenticator {
	t.Helper()
	authenticator, err := identity.NewStaticAPIKeyAuthenticator(map[string]identity.Identity{
		keyTenantA: {
			TenantID:      "tenant-a",
			PrincipalID:   principalA,
			AllowedAppIDs: []string{appAssistant},
		},
		keyTenantB: {
			TenantID:      "tenant-b",
			PrincipalID:   principalB,
			AllowedAppIDs: []string{appAssistant},
		},
	})
	require.NoError(t, err)
	return authenticator
}

func chatHeaders(apiKey string, appID string) map[string]string {
	return map[string]string{
		HeaderAuthorization: "Bearer " + apiKey,
		HeaderAgentAppID:    appID,
	}
}

// barrierDirectory parks every first run inside EnsurePin, so a test can line
// up two requests that each already resolved a different candidate.
type barrierDirectory struct {
	inner       *sessiondir.MemoryDirectory
	arrived     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (d *barrierDirectory) GetPin(
	ctx context.Context,
	key sessiondir.Key,
) (string, bool, error) {
	return d.inner.GetPin(ctx, key)
}

func (d *barrierDirectory) EnsurePin(
	ctx context.Context,
	key sessiondir.Key,
	candidateRevisionID string,
) (string, error) {
	d.arrived <- struct{}{}
	<-d.release
	return d.inner.EnsurePin(ctx, key, candidateRevisionID)
}

// awaitArrival fails fast instead of hanging when a request stops before the
// barrier, which would otherwise look like a deadlock rather than a rejection.
func (d *barrierDirectory) awaitArrival(t *testing.T) {
	t.Helper()
	select {
	case <-d.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("a chat request never reached EnsurePin")
	}
}

func (d *barrierDirectory) releaseAll() {
	d.releaseOnce.Do(func() { close(d.release) })
}

func seedTenantAppRevision(
	t *testing.T,
	handler http.Handler,
	tenantID string,
	appID string,
	revisionID string,
	revisionNo uint64,
	modelName string,
) {
	t.Helper()
	requireStatus(t, handler, http.MethodPost, "/admin/v1/tenants", fmt.Sprintf(`{
		"id":%q,"slug":%q,"name":%q
	}`, tenantID, tenantID, "Tenant "+tenantID), nil, http.StatusCreated)
	requireStatus(
		t, handler, http.MethodPost, "/admin/v1/tenants/"+tenantID+"/apps",
		fmt.Sprintf(`{"id":%q,"name":"Assistant"}`, appID), nil, http.StatusCreated,
	)
	createRevisionThroughAPI(t, handler, tenantID, appID, revisionID, revisionNo, modelName)
	publishRevisionThroughAPI(t, handler, tenantID, appID, revisionID)
}

type resolverFunc func(
	context.Context,
	tenant.TenantContext,
	string,
	string,
) (platformagent.ResolvedRuntime, error)

func (f resolverFunc) Resolve(
	ctx context.Context,
	scope tenant.TenantContext,
	appID string,
	pinnedRevisionID string,
) (platformagent.ResolvedRuntime, error) {
	return f(ctx, scope, appID, pinnedRevisionID)
}

// blockingResponseWriter holds the handler inside its first write so the test
// can observe the lease while the SSE response is still in flight.
type blockingResponseWriter struct {
	header      http.Header
	started     chan struct{}
	released    chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once

	mu     sync.Mutex
	body   bytes.Buffer
	status int
}

func newBlockingResponseWriter() *blockingResponseWriter {
	return &blockingResponseWriter{
		header:   make(http.Header),
		started:  make(chan struct{}),
		released: make(chan struct{}),
	}
}

func (w *blockingResponseWriter) Header() http.Header {
	return w.header
}

func (w *blockingResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
}

func (w *blockingResponseWriter) Write(chunk []byte) (int, error) {
	w.startedOnce.Do(func() { close(w.started) })
	<-w.released
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(chunk)
}

func (w *blockingResponseWriter) Flush() {}

func (w *blockingResponseWriter) release() {
	w.releaseOnce.Do(func() { close(w.released) })
}

func (w *blockingResponseWriter) bodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
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

func doChat(
	t *testing.T,
	handler http.Handler,
	body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	return serve(handler, http.MethodPost, chatPath, body, headers)
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
	response := serve(handler, method, path, body, headers)
	require.Equal(t, expectedStatus, response.Code, response.Body.String())
	return response
}

func serve(
	handler http.Handler,
	method string,
	path string,
	body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func cloneHeaders(headers map[string]string) map[string]string {
	cloned := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		cloned[name] = value
	}
	return cloned
}
