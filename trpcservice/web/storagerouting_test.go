package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storagebundle"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/stretchr/testify/require"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// unavailableRevisionBody is the one answer a caller gets for a revision this
// platform will not serve, whatever the reason. It is a literal rather than a
// constant built from the handler, so a change to the wording has to be made
// here too — the point of the answer is that it never varies.
const unavailableRevisionBody = `{"error":{
	"code":"revision_unavailable",
	"message":"revision is not available"
}}`

const unavailableRuntimeBody = `{"error":{
	"code":"runtime_unavailable",
	"message":"runtime is unavailable"
}}`

// An id that could never name a profile is refused where the revision is
// written. Nothing about it is echoed back.
//
// This is the same refusal every other malformed resource id gets, and it lands
// at create rather than at the first chat because a revision is immutable: one
// that cannot be served has no reason to exist as a draft.
func TestAdminRefusesAMalformedBackendProfileID(t *testing.T) {
	platform := newPlatformTestServer(t)
	seedTenantAndApp(t, platform.handler, "tenant-a", appAssistant)

	for name, profileID := range map[string]string{
		"a traversal":            "../tenant-b-postgres",
		"a separator":            "tenant-a\u0000postgres",
		"blank":                  "   ",
		"a path":                 "tenant-a/postgres",
		"a connection string":    "postgres://user:hunter2@db.internal:5432/sessions",
		"something with a break": "tenant-a-postgres\n",
	} {
		t.Run(name, func(t *testing.T) {
			response := requireStatus(
				t, platform.handler, http.MethodPost,
				"/admin/v1/tenants/tenant-a/apps/assistant/revisions",
				revisionBody(t, "revision-bad", 1, profileID),
				adminHeaders(adminKeyTenantA), http.StatusBadRequest,
			)
			body := response.Body.String()
			require.Contains(t, body, "invalid_argument")
			require.Contains(t, body, "backend profile id")
			// The rejected value never comes back. The connection-string case is
			// why: an admin API that echoes what it was given turns a paste into
			// a disclosure, and a credential is exactly the kind of thing that
			// gets pasted into an id field by accident.
			require.NotContains(t, body, profileID)
			require.NotContains(t, body, "hunter2")
		})
	}

	// And nothing was written: a revision that can never be served must not
	// exist as a draft either.
	requireStatus(t, platform.handler, http.MethodGet,
		"/admin/v1/tenants/tenant-a/apps/assistant/revisions/revision-bad", "",
		adminHeaders(adminKeyTenantA), http.StatusNotFound)
}

// A well-formed profile id is control-plane data, so it is created and published
// like any other field — and then the data plane decides whether this process
// can honour it.
//
// This process cannot: it runs the production source, which has no profiles at
// all. What the caller gets is the same "revision is not available" it gets for
// an unpublished or un-entitled revision, with nothing in it about profiles,
// storage, or which of the three it was. A chat client named no profile; a
// revision did, and which storage a revision runs on is platform configuration
// the caller does not administer.
func TestPlatformRefusesARevisionNamingAProfileThisProcessCannotServe(t *testing.T) {
	const profileID = "tenant-a-postgres"
	platform := newPlatformTestServerWith(t, platformTestOptions{
		stores: productionRouter(t),
	})
	seedTenantAndApp(t, platform.handler, "tenant-a", appAssistant)

	// Created and published: the control plane accepts the reference.
	requireStatus(
		t, platform.handler, http.MethodPost,
		"/admin/v1/tenants/tenant-a/apps/assistant/revisions",
		revisionBody(t, "revision-1", 1, profileID),
		adminHeaders(adminKeyTenantA), http.StatusCreated,
	)
	publishRevisionThroughAPI(t, platform.handler, "tenant-a", appAssistant, "revision-1")

	stored, err := platform.repository.GetRevision(
		context.Background(), tenant.TenantContext{TenantID: "tenant-a"},
		appAssistant, "revision-1")
	require.NoError(t, err)
	require.Equal(t, profileID, stored.Config.BackendProfileID,
		"the reference has to survive the round trip, or this test proves nothing")

	headers := chatHeaders(keyTenantA, appAssistant)
	headers[HeaderSessionID] = "conversation-1"
	response := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"hello"}]
	}`, headers, http.StatusConflict)

	require.JSONEq(t, unavailableRevisionBody, response.Body.String())
	body := response.Body.String()
	for _, leak := range []string{
		profileID, "tenant-a", "storagebundle", "profile", "backend", "revision-1",
	} {
		require.NotContains(t, body, leak)
	}
}

// A storage Router that is shutting down is 503, not 409.
//
// The difference is what the caller should do about it. A profile this process
// cannot serve will still not be servable on the next request, so that is a
// conflict; a Router that is closing means this process is going away, and the
// next request reaches a different one or none. Neither carries a retry hint
// this process is in any position to give.
func TestPlatformReportsAClosedStorageRouterAsUnavailable(t *testing.T) {
	router := productionRouter(t)
	platform := newPlatformTestServerWith(t, platformTestOptions{stores: router})
	seedTenantAppRevision(t, platform.handler, "tenant-a", appAssistant, "revision-1", 1, "echo-v1")

	// Closed before any Runtime was built, so nothing holds a lease and Close
	// returns rather than waiting. This is the state a process is in between
	// "shutdown started" and "the listener stopped accepting".
	require.NoError(t, router.Close())

	headers := chatHeaders(keyTenantA, appAssistant)
	headers[HeaderSessionID] = "conversation-1"
	response := requireStatus(t, platform.handler, http.MethodPost, chatPath, `{
		"model":"ignored","messages":[{"role":"user","content":"hello"}]
	}`, headers, http.StatusServiceUnavailable)

	require.JSONEq(t, unavailableRuntimeBody, response.Body.String())
	require.Empty(t, response.Header().Get(HeaderRetryAfter),
		"a process that is going away cannot say when to come back")
}

// The mapping itself, over every sentinel the storage layer defines.
//
// It is a table rather than six end-to-end tests because what is under test is
// the switch: which sentinel lands on which status, and — the part that is easy
// to break — that the storage cases are reached before the invalid_argument one.
// The errors are wrapped the way agent.newRuntime wraps them, so a case that
// only matched an unwrapped sentinel would fail here.
func TestWriteDomainErrorMapsStorageRouting(t *testing.T) {
	// The genuine article: Profile.Validate wraps ErrInvalidProfile *and*
	// tenant.ErrInvalidArgument, so this error satisfies both. It is the reason
	// the storage cases sit above the invalid_argument case — matched there
	// instead, it would answer 400 with err.Error() in the body, which is the
	// profile id and the tenant that owns it.
	profileErr := storagebundle.Profile{
		TenantID: "tenant-a",
		ID:       "../../etc/passwd",
	}.Validate()
	require.ErrorIs(t, profileErr, storagebundle.ErrInvalidProfile)
	require.ErrorIs(t, profileErr, tenant.ErrInvalidArgument,
		"the ordering this test pins would be pointless if this were not true")

	for name, tc := range map[string]struct {
		err    error
		status int
		body   string
	}{
		"a profile this tenant does not have": {
			err: fmt.Errorf("%w: %q",
				storagebundle.ErrProfileNotFound, "tenant-a-postgres"),
			status: http.StatusConflict,
			body:   unavailableRevisionBody,
		},
		"content that moved under an immutable id": {
			err: fmt.Errorf("%w: backend profile %q of tenant %q",
				storagebundle.ErrProfileChanged, "tenant-a-postgres", "tenant-a"),
			status: http.StatusConflict,
			body:   unavailableRevisionBody,
		},
		"a profile that is not well formed": {
			err:    profileErr,
			status: http.StatusConflict,
			body:   unavailableRevisionBody,
		},
		"a backend this build cannot construct": {
			err: fmt.Errorf("%w: %q",
				storagebundle.ErrUnsupportedBackend, "postgres"),
			status: http.StatusConflict,
			body:   unavailableRevisionBody,
		},
		"a durable backend without durable pins": {
			err:    storagebundle.ErrPinsNotDurable,
			status: http.StatusConflict,
			body:   unavailableRevisionBody,
		},
		"an in-process backend across workers": {
			err:    storagebundle.ErrNotSharedAcrossWorkers,
			status: http.StatusConflict,
			body:   unavailableRevisionBody,
		},
		"a router that is shutting down": {
			err:    storagebundle.ErrRouterClosed,
			status: http.StatusServiceUnavailable,
			body:   unavailableRuntimeBody,
		},
		"a runtime resolver that is shutting down": {
			err:    platformagent.ErrResolverClosed,
			status: http.StatusServiceUnavailable,
			body:   unavailableRuntimeBody,
		},
		// Not a refusal at all. A Factory that produced a Bundle with nothing in
		// it broke this platform's own contract, and there is no wording that
		// makes that the caller's problem — it has to be a 500, or the defect
		// reads like an ordinary rejected request in the logs.
		"a bundle this platform built wrong": {
			err:    storagebundle.ErrIncompleteBundle,
			status: http.StatusInternalServerError,
			body:   `{"error":{"code":"internal_error","message":"internal server error"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			// Wrapped the way the runtime build wraps it: by the time this
			// reaches the writer it has a revision id and a sentence in front of
			// it, and none of that may change the answer.
			wrapped := fmt.Errorf(
				"agent: resolve session store for revision %q: %w", "revision-1", tc.err)

			for label, err := range map[string]error{"bare": tc.err, "wrapped": wrapped} {
				t.Run(label, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					writeDomainError(recorder, err)
					require.Equal(t, tc.status, recorder.Code)
					require.JSONEq(t, tc.body, recorder.Body.String())
					require.NotContains(t, recorder.Body.String(), "revision-1",
						"the wrapper's own words reached the caller")
				})
			}
		})
	}
}

// The 409 answers are byte-identical to each other and to the ones a client
// already gets for a revision that is merely unpublished.
//
// That is the property, not an accident of six cases sharing a literal: any
// difference between them tells a caller which of the reasons applied, and
// several of those reasons are facts about this platform's storage
// configuration.
func TestStorageRefusalsAreIndistinguishableFromOtherRevisionRefusals(t *testing.T) {
	answers := map[string]string{}
	for name, err := range map[string]error{
		"profile not found":   storagebundle.ErrProfileNotFound,
		"profile changed":     storagebundle.ErrProfileChanged,
		"invalid profile":     storagebundle.ErrInvalidProfile,
		"unsupported backend": storagebundle.ErrUnsupportedBackend,
		"pins not durable":    storagebundle.ErrPinsNotDurable,
		"not shared":          storagebundle.ErrNotSharedAcrossWorkers,
		// The answer that already existed, and the one the six above had to be
		// made to match: a revision whose stored bytes no longer hash to their
		// digest is refused in exactly these words.
		"config integrity": tenant.ErrConfigIntegrity,
	} {
		recorder := httptest.NewRecorder()
		writeDomainError(recorder, err)
		require.Equal(t, http.StatusConflict, recorder.Code, name)
		answers[name] = recorder.Body.String()
	}

	var first string
	for name, answer := range answers {
		if first == "" {
			first = answer
			continue
		}
		require.Equal(t, first, answer, "%s is distinguishable from the others", name)
	}
	require.JSONEq(t, unavailableRevisionBody, first)

	// The unpublished-revision answer is the same status and the same code. It
	// is not byte-identical — it carries its own sentence — and that is the
	// existing behaviour, not something this change is free to alter.
	unpublished := httptest.NewRecorder()
	writeDomainError(unpublished, tenant.ErrRevisionNotPublished)
	require.Equal(t, http.StatusConflict, unpublished.Code)
	require.Contains(t, unpublished.Body.String(), `"code":"revision_unavailable"`)
}

// productionRouter is the storage arrangement the binary boots with: one process
// default, no dynamic profiles, and the session factory this slice ships.
//
// Its Close is registered here, before the caller builds a platform around it,
// so the resolver's own Cleanup — registered later and therefore run first —
// has already released every lease by the time this one waits for them.
func productionRouter(t *testing.T) *storagebundle.Router {
	t.Helper()
	sessions := sessioninmemory.NewSessionService()
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })

	router, err := storagebundle.NewRouter(storagebundle.Options{
		Default: storagebundle.Bundle{Session: sessions},
		Source:  storagebundle.NoProfiles(),
		Factory: storagebundle.NewSessionFactory(storagebundle.ProcessConstraints{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, router.Close()) })
	return router
}

// seedTenantAndApp creates the two records a revision needs to exist, and
// nothing else. It is seedTenantAppRevision without the revision, for the tests
// that have to author that part themselves.
func seedTenantAndApp(t *testing.T, handler http.Handler, tenantID string, appID string) {
	t.Helper()
	requireStatus(t, handler, http.MethodPost, "/admin/v1/tenants", fmt.Sprintf(`{
		"id":%q,"slug":%q,"name":%q
	}`, tenantID, tenantID, "Tenant "+tenantID),
		adminHeaders(adminKeyPlatform), http.StatusCreated)
	requireStatus(
		t, handler, http.MethodPost, "/admin/v1/tenants/"+tenantID+"/apps",
		fmt.Sprintf(`{"id":%q,"name":"Assistant"}`, appID),
		adminHeaders(adminKeyPlatform), http.StatusCreated,
	)
}

// revisionBody is a create-revision request that names a backend profile.
//
// It is encoded rather than formatted. Several of the ids these tests send are
// malformed on purpose — a NUL, a newline — and only a real encoder puts those
// on the wire as the bytes the handler is supposed to reject.
func revisionBody(t *testing.T, revisionID string, revisionNo uint64, profileID string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":          revisionID,
		"revision_no": revisionNo,
		"config": map[string]any{
			"agent_name":         "test-agent",
			"instruction":        "Answer through the deterministic model.",
			"model":              map[string]any{"provider": "deterministic", "name": "echo-v1"},
			"backend_profile_id": profileID,
		},
	})
	require.NoError(t, err)
	return string(body)
}
