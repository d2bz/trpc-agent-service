package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	openaiserver "trpc.group/trpc-go/trpc-agent-go/server/openai"
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

	adaptersMu sync.Mutex
	adapters   map[*platformagent.Runtime]*openaiserver.Server
	closed     bool
	closeOnce  sync.Once
	closeErr   error
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
		adapters:   make(map[*platformagent.Runtime]*openaiserver.Server),
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

func (s *PlatformServer) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.adaptersMu.Lock()
		s.closed = true
		adapters := make([]*openaiserver.Server, 0, len(s.adapters))
		for runtime, adapter := range s.adapters {
			adapters = append(adapters, adapter)
			delete(s.adapters, runtime)
		}
		s.adaptersMu.Unlock()
		for _, adapter := range adapters {
			s.closeErr = errors.Join(s.closeErr, adapter.Close())
		}
	})
	return s.closeErr
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

	adapter, err := s.adapterFor(resolved.Runtime)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	adapter.Handler().ServeHTTP(w, r)
}

func (s *PlatformServer) adapterFor(runtime *platformagent.Runtime) (*openaiserver.Server, error) {
	if runtime == nil || runtime.Runner == nil || runtime.SessionService == nil ||
		runtime.AppName == "" || runtime.ModelName == "" {
		return nil, fmt.Errorf("web: incomplete resolved runtime")
	}
	s.adaptersMu.Lock()
	defer s.adaptersMu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("web: platform server is closed")
	}
	if adapter := s.adapters[runtime]; adapter != nil {
		return adapter, nil
	}
	adapter, err := openaiserver.New(
		openaiserver.WithRunner(runtime.Runner),
		openaiserver.WithSessionService(runtime.SessionService),
		openaiserver.WithAppName(runtime.AppName),
		openaiserver.WithModelName(runtime.ModelName),
		openaiserver.WithBasePath("/v1"),
	)
	if err != nil {
		return nil, fmt.Errorf("web: create runtime OpenAI adapter: %w", err)
	}
	s.adapters[runtime] = adapter
	return adapter, nil
}
