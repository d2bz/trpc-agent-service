package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/stretchr/testify/require"
)

func TestHealth(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"status":"ok"}`, recorder.Body.String())
}

func TestChatCompletion(t *testing.T) {
	server := newTestServer(t)
	body := `{"model":"deterministic-echo","messages":[{"role":"user","content":"hello"}]}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Session-ID", "test-session")
	server.Handler().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "echo: hello")
	require.Contains(t, recorder.Body.String(), platformagent.DemoModelName)
}

func TestStreamingChatCompletion(t *testing.T) {
	server := newTestServer(t)
	body := `{"model":"deterministic-echo","stream":true,"messages":[{"role":"user","content":"stream"}]}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, recorder.Body.String(), "echo: ")
	require.Contains(t, recorder.Body.String(), "stream")
	require.Contains(t, recorder.Body.String(), "data: [DONE]")
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	runtime := platformagent.NewDemoRuntime()
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	server, err := NewServer(runtime)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	return server
}
