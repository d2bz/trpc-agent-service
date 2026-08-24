// Package web exposes the platform HTTP endpoints.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	openaiserver "trpc.group/trpc-go/trpc-agent-go/server/openai"
)

// Server combines platform endpoints with the upstream OpenAI-compatible API.
type Server struct {
	handler http.Handler
	openAI  *openaiserver.Server
}

// NewServer constructs the HTTP surface around an existing runtime.
func NewServer(runtime *platformagent.Runtime) (*Server, error) {
	if runtime == nil || runtime.Runner == nil || runtime.SessionService == nil ||
		runtime.AppName == "" || runtime.ModelName == "" {
		return nil, fmt.Errorf("web: incomplete agent runtime")
	}
	openAI, err := openaiserver.New(
		openaiserver.WithRunner(runtime.Runner),
		openaiserver.WithSessionService(runtime.SessionService),
		openaiserver.WithAppName(runtime.AppName),
		openaiserver.WithModelName(runtime.ModelName),
		openaiserver.WithBasePath("/v1"),
	)
	if err != nil {
		return nil, fmt.Errorf("web: create OpenAI-compatible server: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.Handle("/v1/", openAI.Handler())
	return &Server{handler: mux, openAI: openAI}, nil
}

// Handler returns the service HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Close releases resources owned by the protocol adapter. The caller owns the
// Runtime and closes it separately.
func (s *Server) Close() error {
	if s == nil || s.openAI == nil {
		return nil
	}
	return s.openAI.Close()
}
