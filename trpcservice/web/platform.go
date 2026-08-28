package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

const (
	HeaderTenantID        = "X-Tenant-ID"
	HeaderAgentAppID      = "X-Agent-App-ID"
	HeaderAgentRevisionID = "X-Agent-Revision-ID"
	HeaderSessionID       = "X-Session-ID"
	HeaderAuthorization   = "Authorization"
)

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
type PlatformServer struct {
	repository    tenant.Repository
	resolver      runtimeResolver
	authenticator identity.Authenticator
	sessions      sessiondir.Directory
	handler       http.Handler
}

func NewPlatformServer(
	repository tenant.Repository,
	resolver runtimeResolver,
	authenticator identity.Authenticator,
	sessions sessiondir.Directory,
) (*PlatformServer, error) {
	if repository == nil {
		return nil, fmt.Errorf("web: tenant repository is required")
	}
	if resolver == nil {
		return nil, fmt.Errorf("web: runtime resolver is required")
	}
	if authenticator == nil {
		return nil, fmt.Errorf("web: chat authenticator is required")
	}
	if sessions == nil {
		return nil, fmt.Errorf("web: session directory is required")
	}
	server := &PlatformServer{
		repository:    repository,
		resolver:      resolver,
		authenticator: authenticator,
		sessions:      sessions,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/admin/v1/tenants", server.handleAdmin)
	mux.HandleFunc("/admin/v1/tenants/", server.handleAdmin)
	mux.HandleFunc("/v1/chat/completions", server.handleChatCompletions)
	server.handler = mux
	return server, nil
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
	runContext, err := identity.WithRunContext(r.Context(), identity.RunContext{
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
	// id still needs it back from a 400 or a 500 answer.
	writeChatResponseHeaders(w, key.SessionID, resolved.Revision.ID)
	// Keep the adapter's own view of the session consistent with the pinned
	// one. contextRunner ignores this header either way.
	r.Header.Set(HeaderSessionID, key.SessionID)
	handler.ServeHTTP(w, r.WithContext(runContext))
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
	caller, err := s.authenticator.Authenticate(r.Context(), token)
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
	header.Set("Access-Control-Expose-Headers", HeaderSessionID+", "+HeaderAgentRevisionID)
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
