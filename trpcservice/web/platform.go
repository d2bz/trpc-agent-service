package web

import (
	"context"
	"fmt"
	"net/http"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

const (
	HeaderTenantID        = "X-Tenant-ID"
	HeaderAgentAppID      = "X-Agent-App-ID"
	HeaderAgentRevisionID = "X-Agent-Revision-ID"
)

type runtimeResolver interface {
	Resolve(
		context.Context,
		tenant.TenantContext,
		string,
		string,
	) (platformagent.ResolvedRuntime, error)
}

// PlatformServer exposes the control plane and dynamically routes chat traffic.
type PlatformServer struct {
	repository tenant.Repository
	resolver   runtimeResolver
	handler    http.Handler
}

func NewPlatformServer(
	repository tenant.Repository,
	resolver runtimeResolver,
) (*PlatformServer, error) {
	if repository == nil {
		return nil, fmt.Errorf("web: tenant repository is required")
	}
	if resolver == nil {
		return nil, fmt.Errorf("web: runtime resolver is required")
	}
	server := &PlatformServer{
		repository: repository,
		resolver:   resolver,
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

func (s *PlatformServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
		w.Header().Set(
			"Access-Control-Allow-Headers",
			"Content-Type, Authorization, "+HeaderTenantID+", "+HeaderAgentAppID+", "+HeaderAgentRevisionID,
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, http.MethodOptions)
		return
	}
	tenantID := r.Header.Get(HeaderTenantID)
	appID := r.Header.Get(HeaderAgentAppID)
	revisionID := r.Header.Get(HeaderAgentRevisionID)
	if tenantID == "" || appID == "" {
		writeAPIError(
			w,
			http.StatusBadRequest,
			"missing_route",
			HeaderTenantID+" and "+HeaderAgentAppID+" are required",
		)
		return
	}
	resolved, err := s.resolver.Resolve(
		r.Context(),
		tenant.TenantContext{TenantID: tenantID},
		appID,
		revisionID,
	)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	defer resolved.Release()

	handler, err := resolved.Runtime.OpenAIHandler()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	handler.ServeHTTP(w, r)
}
