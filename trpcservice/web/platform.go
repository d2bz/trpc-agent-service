package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	"github.com/liuzengh/trpc-agent-service/trpcservice/security"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessionlease"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

const (
	HeaderTenantID        = "X-Tenant-ID"
	HeaderAgentAppID      = "X-Agent-App-ID"
	HeaderAgentRevisionID = "X-Agent-Revision-ID"
	HeaderSessionID       = "X-Session-ID"
	HeaderAuthorization   = "Authorization"
	HeaderRetryAfter      = "Retry-After"
)

// busyRetryAfter is what a 409 tells the client to wait. It is short because
// session_busy is usually one impatient client with two tabs open, not a queue:
// the other run is expected to finish in seconds, and there is no waiting list
// to join in the meantime.
const busyRetryAfter = 2 * time.Second

// releaseTimeout bounds giving the lease back on a clean finish. The response
// has already been written by then, so this cannot make the client wait for
// anything it can see — it only stops a stalled coordinator from holding the
// connection open. A release that does not make it inside the budget costs
// nothing but the remainder of the lease's TTL.
const releaseTimeout = 2 * time.Second

// bearerScheme is matched case-insensitively, as RFC 7235 requires.
const bearerScheme = "bearer"

// resourceIDSyntax restates the shared resource id constraint in client terms.
const resourceIDSyntax = "1-128 characters of [A-Za-z0-9._-] starting with a letter or digit"

type runtimeResolver interface {
	Resolve(
		context.Context,
		tenant.TenantContext,
		string,
		string,
	) (platformagent.ResolvedRuntime, error)
}

// PlatformServer exposes the control plane and routes authenticated chat
// traffic to the revision each session is pinned to.
//
// The two authenticators are separate fields of separate types, so a chat
// credential cannot reach an admin route and an admin credential cannot reach a
// chat one. That isolation is a property of the type system here, not of a
// comparison somewhere in a handler.
type PlatformServer struct {
	repository tenant.Repository
	resolver   runtimeResolver
	chat       identity.Authenticator
	admin      identity.AdminAuthenticator
	revisions  security.RevisionAuthorizer
	sessions   sessiondir.Directory
	leases     sessionlease.Coordinator
	handler    http.Handler
}

// NewPlatformServer builds the server. Every dependency is a parameter,
// including the run-lease coordinator: a process that forgot to configure
// coordination must fail to build rather than fall back to a package-level
// default that coordinates nothing, which is precisely the deployment where two
// Workers end up running one Session.
//
// The same applies to the admin authenticator and the revision authorizer.
// Neither has a permissive default and neither may be nil: a control plane that
// started with no way to authenticate, or with no opinion on which revisions
// may run, is a control plane whose safety depends on nobody finding it.
func NewPlatformServer(
	repository tenant.Repository,
	resolver runtimeResolver,
	chatAuthenticator identity.Authenticator,
	adminAuthenticator identity.AdminAuthenticator,
	revisionAuthorizer security.RevisionAuthorizer,
	sessions sessiondir.Directory,
	leases sessionlease.Coordinator,
) (*PlatformServer, error) {
	if repository == nil {
		return nil, fmt.Errorf("web: tenant repository is required")
	}
	if resolver == nil {
		return nil, fmt.Errorf("web: runtime resolver is required")
	}
	if chatAuthenticator == nil {
		return nil, fmt.Errorf("web: chat authenticator is required")
	}
	if adminAuthenticator == nil {
		return nil, fmt.Errorf("web: admin authenticator is required")
	}
	if revisionAuthorizer == nil {
		return nil, fmt.Errorf("web: revision authorizer is required")
	}
	if sessions == nil {
		return nil, fmt.Errorf("web: session directory is required")
	}
	if leases == nil {
		return nil, fmt.Errorf("web: session lease coordinator is required")
	}
	server := &PlatformServer{
		repository: repository,
		resolver:   resolver,
		chat:       chatAuthenticator,
		admin:      adminAuthenticator,
		revisions:  revisionAuthorizer,
		sessions:   sessions,
		leases:     leases,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/v1/chat/completions", server.handleChatCompletions)
	// The admin subtree is deliberately not registered on the mux: it is taken
	// before the mux runs at all. See adminFirst.
	server.handler = adminFirst(mux, server.handleAdmin)
	return server, nil
}

// adminFirst puts the control-plane trust boundary in front of the router.
//
// The whole /admin subtree is one handler, not just the paths that exist. Left
// to a router, every other admin path would be answered by the router itself —
// before anything has authenticated — so "does this admin route exist" would be
// a question any unauthenticated caller could ask.
//
// http.ServeMux cannot be that router, because it answers about /admin before
// it dispatches: it cleans the request path first, and a path that changes
// under cleaning gets a 301 to the cleaned form no matter what is registered.
// /admin//v1/tenants, /admin/./v1/tenants and /admin/v1/tenants/../secrets are
// each a redirect written for a caller holding no credential. So the prefix is
// matched here, on the raw URL.Path, and handleAdmin runs before the mux ever
// sees the request.
//
// handleAdmin then routes on that same raw path by exact comparison, which is
// what makes this safe rather than merely quiet: no traversal is resolved on
// the way in, so a path that spells a real route oddly is not that route. It
// authenticates, and is then a 404 like any other name that is not there.
func adminFirst(mux http.Handler, admin http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAdminPath(r.URL.Path) {
			admin(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// isAdminPath reports whether path addresses the control plane. The bare
// prefix is included: /admin is answered, never redirected to /admin/.
func isAdminPath(path string) bool {
	return path == adminPathPrefix || strings.HasPrefix(path, adminPathPrefix+"/")
}

func (s *PlatformServer) Handler() http.Handler {
	return s.handler
}

// handleChatCompletions derives the whole run scope from the credential. The
// request body and the routing headers can only narrow that scope or be
// rejected; they can never widen it.
func (s *PlatformServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	// Every chat response carries these, including the ones that fail before
	// routing: without them a browser cannot read the documented JSON error of a
	// 401, 403, 400 or 409 answer, only an opaque CORS failure.
	writeChatCORSHeaders(w)
	if r.Method == http.MethodOptions {
		writeChatPreflight(w)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, http.MethodOptions)
		return
	}
	caller, ok := s.authenticateChat(w, r)
	if !ok {
		return
	}
	// X-Tenant-ID is an assertion a client may make about the credential it
	// used. It never selects the tenant, so a mismatch is a refusal.
	if asserted := r.Header.Get(HeaderTenantID); asserted != "" && asserted != caller.TenantID {
		writeAPIError(
			w,
			http.StatusForbidden,
			"forbidden",
			HeaderTenantID+" does not match the authenticated tenant",
		)
		return
	}
	appID := r.Header.Get(HeaderAgentAppID)
	if err := tenant.ValidateResourceID("app id", appID); err != nil {
		writeAPIError(
			w,
			http.StatusBadRequest,
			"missing_route",
			HeaderAgentAppID+" is required and must be "+resourceIDSyntax,
		)
		return
	}
	if !caller.AllowsApp(appID) {
		writeAPIError(
			w,
			http.StatusForbidden,
			"forbidden",
			"this credential may not call the requested agent app",
		)
		return
	}
	sessionID, ok := chatSessionID(w, r)
	if !ok {
		return
	}
	key := sessiondir.Key{
		TenantID:    caller.TenantID,
		AppID:       appID,
		PrincipalID: caller.PrincipalID,
		SessionID:   sessionID,
	}
	// The lease is taken before the pin is read and before a Runtime is built.
	// Everything after this point writes to the session, and a request that is
	// going to be refused must not have created a pin or leased a Runtime on the
	// way to being refused.
	lease, ok := s.acquireRunLease(w, r, key)
	if !ok {
		return
	}
	// runCtx is the request's context with one more way to end: losing the
	// lease. Cancelling it is the whole of what this platform can do about a
	// takeover — see the sessionlease package documentation for what that does
	// and does not stop.
	runCtx, cancelRun := context.WithCancel(r.Context())
	defer cancelRun()
	// An independent watcher rather than a check inside the streaming loop. The
	// SSE writer blocks for as long as the client takes to read, and renewal
	// runs in the coordinator's own goroutine, so neither of them is anywhere a
	// lost lease could be noticed promptly. This goroutine ends with the run.
	go func() {
		select {
		case <-lease.Done():
			cancelRun()
		case <-runCtx.Done():
		}
	}()
	r = r.WithContext(runCtx)
	// Registered after cancelRun, so it runs before it: whether this was a clean
	// finish is decided while runCtx still says so. ServeHTTP returning is the
	// boundary — that is what "the run is over" means here.
	defer releaseRunLease(runCtx, lease)

	resolved, ok := s.pinnedRuntime(w, r, key)
	if !ok {
		return
	}
	defer resolved.Release()

	handler, err := resolved.Runtime.OpenAIHandler()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	runContext, err := identity.WithRunContext(runCtx, identity.RunContext{
		TenantID:    key.TenantID,
		AppID:       key.AppID,
		PrincipalID: key.PrincipalID,
		SessionID:   key.SessionID,
		RevisionID:  resolved.Revision.ID,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	// The adapter owns the status line from here on, so every response header
	// has to be in place first: a client that let the platform mint a session
	// id still needs it back from a 400 or a 500 answer. The fence is not among
	// them: it is an observation handle, it admits nothing, and publishing it
	// would invite a client to treat it as a guarantee it is not.
	writeChatResponseHeaders(w, key.SessionID, resolved.Revision.ID)
	// Keep the adapter's own view of the session consistent with the pinned
	// one. contextRunner ignores this header either way.
	r.Header.Set(HeaderSessionID, key.SessionID)
	handler.ServeHTTP(w, r.WithContext(runContext))
}

// acquireRunLease takes the run lease for this request, or answers the client.
//
// There are two answers, and the difference matters to a client deciding what to
// do next. 409 means the platform is healthy and someone else is running this
// session: the same request will work shortly. 503 means coordination could not
// tell us anything, so the run was not started.
//
// Everything that is not a definite "busy" ends in 503, including a backend
// reply this build does not recognise. That is the fail-closed half of the
// contract: guessing that an unreadable answer meant "free" is how two Workers
// end up in one Session, and the whole of this feature is the promise that they
// do not.
func (s *PlatformServer) acquireRunLease(
	w http.ResponseWriter,
	r *http.Request,
	key sessiondir.Key,
) (sessionlease.Lease, bool) {
	lease, err := s.leases.Acquire(r.Context(), key)
	if err == nil {
		return lease, true
	}
	if errors.Is(err, sessionlease.ErrSessionBusy) {
		w.Header().Set(HeaderRetryAfter, strconv.Itoa(int(busyRetryAfter.Seconds())))
		writeAPIError(w, http.StatusConflict, "session_busy", fmt.Sprintf(
			"session %q is already being run; retry when that run finishes",
			key.SessionID,
		))
		return nil, false
	}
	// A cancelled request context arrives here too, and shares this answer. It
	// is reported distinctly by the coordinator, but there is no separate HTTP
	// status for "the caller stopped listening", and no caller left to read one.
	writeAPIError(
		w,
		http.StatusServiceUnavailable,
		"coordination_unavailable",
		"run coordination is unavailable; this request was not started",
	)
	return nil, false
}

// releaseRunLease gives the lease back, but only after a clean finish.
//
// The asymmetry is deliberate. Releasing makes the session immediately available
// to the next Worker, which is right when this one has genuinely stopped — but
// an upstream Runner whose context was just cancelled keeps emitting terminal
// events for about a second afterwards, on a context this process cannot reach.
// Deleting the lock in that moment would hand the session to another Worker
// while the previous run is still writing to it. Leaving it to expire by TTL
// costs the next caller a few seconds and covers exactly that tail.
//
// So: a run that ended by itself releases; a run that ended because the client
// disconnected, the lease was lost, or the process is shutting down does not.
func releaseRunLease(runCtx context.Context, lease sessionlease.Lease) {
	select {
	case <-lease.Done():
		// Lost or taken over. There is nothing of ours left to give back, and a
		// Release cannot disturb a new owner anyway — it is owner-matched — but
		// the TTL is what covers our own tail writes, so leave it alone.
		return
	default:
	}
	if runCtx.Err() != nil {
		return
	}
	// Independent of the request: the response is written and the client may
	// already be gone, and this still has to complete.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), releaseTimeout)
	defer cancel()
	// The result is deliberately discarded. The status line and the body left
	// this process long ago — an SSE stream has been flushed turn by turn — so
	// there is nothing left to tell the client and nothing to correct. A release
	// that failed leaves a lock that expires by TTL, which is the same state a
	// crash would have left, and the next Acquire recovers from it.
	_ = lease.Release(ctx)
}

func (s *PlatformServer) authenticateChat(
	w http.ResponseWriter,
	r *http.Request,
) (identity.Identity, bool) {
	token, ok := bearerToken(r.Header.Get(HeaderAuthorization))
	if !ok {
		writeUnauthenticated(w, "a Bearer credential is required")
		return identity.Identity{}, false
	}
	caller, err := s.chat.Authenticate(r.Context(), token)
	if err != nil {
		if errors.Is(err, identity.ErrForbidden) {
			writeAPIError(w, http.StatusForbidden, "forbidden", "this credential is not allowed")
			return identity.Identity{}, false
		}
		writeUnauthenticated(w, "the credential is not valid")
		return identity.Identity{}, false
	}
	if err := caller.Validate(); err != nil {
		writeUnauthenticated(w, "the credential is not valid")
		return identity.Identity{}, false
	}
	return caller, true
}

// pinnedRuntime resolves the revision this session is pinned to. A session
// without a pin adopts the revision of its first run, and X-Agent-Revision-ID
// is a development hint for that first run only: it can never move a session
// that is already pinned. Every failure path releases the lease it took.
func (s *PlatformServer) pinnedRuntime(
	w http.ResponseWriter,
	r *http.Request,
	key sessiondir.Key,
) (platformagent.ResolvedRuntime, bool) {
	revisionHint := strings.TrimSpace(r.Header.Get(HeaderAgentRevisionID))
	if revisionHint != "" {
		if err := tenant.ValidateResourceID("revision id", revisionHint); err != nil {
			writeDomainError(w, err)
			return platformagent.ResolvedRuntime{}, false
		}
	}
	ctx := r.Context()
	scope := tenant.TenantContext{TenantID: key.TenantID}
	pinned, found, err := s.sessions.GetPin(ctx, key)
	if err != nil {
		writeDomainError(w, err)
		return platformagent.ResolvedRuntime{}, false
	}
	if found {
		if revisionHint != "" && revisionHint != pinned {
			writeAPIError(w, http.StatusConflict, "pin_conflict", fmt.Sprintf(
				"session %q is pinned to revision %q", key.SessionID, pinned,
			))
			return platformagent.ResolvedRuntime{}, false
		}
		resolved, resolveErr := s.resolver.Resolve(ctx, scope, key.AppID, pinned)
		if resolveErr != nil {
			writeDomainError(w, resolveErr)
			return platformagent.ResolvedRuntime{}, false
		}
		return resolved, true
	}

	// Only a revision that actually resolved may become a candidate, so a
	// session can never be pinned to a revision this platform cannot serve.
	candidate, err := s.resolver.Resolve(ctx, scope, key.AppID, revisionHint)
	if err != nil {
		writeDomainError(w, err)
		return platformagent.ResolvedRuntime{}, false
	}
	winner, err := s.sessions.EnsurePin(ctx, key, candidate.Revision.ID)
	if err != nil {
		candidate.Release()
		writeDomainError(w, err)
		return platformagent.ResolvedRuntime{}, false
	}
	if winner == candidate.Revision.ID {
		return candidate, true
	}
	// A concurrent first run won the pin. Drop the losing lease before taking
	// the winner's, otherwise this request keeps a Runtime alive that it will
	// never use.
	candidate.Release()
	resolved, err := s.resolver.Resolve(ctx, scope, key.AppID, winner)
	if err != nil {
		writeDomainError(w, err)
		return platformagent.ResolvedRuntime{}, false
	}
	return resolved, true
}

// chatSessionID returns the client session id, or a new one the platform owns.
// A generated id is echoed back in every response so the caller can continue
// the same conversation.
func chatSessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	sessionID := strings.TrimSpace(r.Header.Get(HeaderSessionID))
	if sessionID == "" {
		return uuid.NewString(), true
	}
	if err := tenant.ValidateResourceID("session id", sessionID); err != nil {
		writeAPIError(
			w,
			http.StatusBadRequest,
			"invalid_session_id",
			HeaderSessionID+" must be "+resourceIDSyntax,
		)
		return "", false
	}
	return sessionID, true
}

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

func writeUnauthenticated(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeAPIError(w, http.StatusUnauthorized, "unauthenticated", message)
}

// writeChatCORSHeaders publishes what every chat response shares, preflight and
// actual alike. It runs before authentication, so the browser of a caller that
// was refused can still read why.
func writeChatCORSHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Access-Control-Allow-Origin", "*")
	// Retry-After is exposed because it is the actionable part of a 409: a
	// browser client that cannot read it has been told "busy" with no idea when
	// to come back, and will either give up or retry immediately.
	header.Set("Access-Control-Expose-Headers", strings.Join([]string{
		HeaderSessionID,
		HeaderAgentRevisionID,
		HeaderRetryAfter,
	}, ", "))
}

// writeChatResponseHeaders publishes the run scope. It stays at the point where
// the session and the revision are actually known: a request refused earlier has
// no revision to name, and would be pinned to nothing.
func writeChatResponseHeaders(w http.ResponseWriter, sessionID string, revisionID string) {
	header := w.Header()
	header.Set(HeaderSessionID, sessionID)
	header.Set(HeaderAgentRevisionID, revisionID)
}

// writeChatPreflight stays unauthenticated: a browser cannot attach the
// credential to a preflight request.
func writeChatPreflight(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Access-Control-Allow-Methods", http.MethodPost)
	header.Set("Access-Control-Allow-Headers", strings.Join([]string{
		"Content-Type",
		HeaderAuthorization,
		HeaderTenantID,
		HeaderAgentAppID,
		HeaderAgentRevisionID,
		HeaderSessionID,
	}, ", "))
	w.WriteHeader(http.StatusNoContent)
}
