package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	platformconfig "github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/liuzengh/trpc-agent-service/trpcservice/web"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const shutdownTimeout = 10 * time.Second

const (
	// apiKeyEnvVar overrides the chat credential of the local process.
	apiKeyEnvVar = "TRPC_SERVICE_API_KEY"
	// developmentAPIKey is a published placeholder, not a secret. It exists so
	// the demo runs without configuration and is safe only because
	// validateListenAddr keeps the process on a loopback address.
	developmentAPIKey = "local-development-key-not-a-secret"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("trpc-agent-service %s\n", trpcservice.Version)
		return
	}
	if err := run(*addr); err != nil {
		log.Fatalf("trpc-agent-service: %v", err)
	}
}

func run(addr string) error {
	if err := validateListenAddr(addr); err != nil {
		return err
	}
	repository := tenant.NewMemoryRepository()
	if err := platformconfig.SeedDemo(context.Background(), repository); err != nil {
		return err
	}
	sessionService := sessioninmemory.NewSessionService()
	defer func() {
		if err := sessionService.Close(); err != nil {
			log.Printf("close session service: %v", err)
		}
	}()

	resolver, err := platformagent.NewRuntimeResolver(
		repository,
		func(
			_ context.Context,
			revision tenant.AgentRevision,
		) (*platformagent.Runtime, error) {
			return platformagent.NewRuntimeFromRevision(revision, sessionService)
		},
	)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resolver.Close(); closeErr != nil {
			log.Printf("close runtime resolver: %v", closeErr)
		}
	}()

	authenticator, err := demoAuthenticator()
	if err != nil {
		return err
	}
	api, err := web.NewPlatformServer(
		repository,
		resolver,
		authenticator,
		sessiondir.NewMemoryDirectory(),
	)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf(
			"trpc-agent-service %s listening on %s; chat=/v1/chat/completions admin=/admin/v1/tenants",
			trpcservice.Version,
			addr,
		)
		errCh <- httpServer.ListenAndServe()
	}()

	signalCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	return waitForStop(signalCtx, errCh, httpServer, shutdownTimeout)
}

// loopbackHostname is the only non-literal host accepted as loopback. Resolving
// arbitrary names would make the guard depend on whatever the resolver answers
// at startup, so anything else is refused even if it happens to point at 127/8.
const loopbackHostname = "localhost"

// validateListenAddr fails closed on every address that is not loopback. The
// Admin API carries no authentication at all, so a wildcard or routable bind
// would publish tenant creation and revision publishing to the network. This is
// deliberately not overridable: the override belongs in the slice that adds
// Admin authentication, not in this one.
func validateListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if strings.EqualFold(host, loopbackHostname) {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	// An empty host is the wildcard form (":8080"), which is the easiest way to
	// expose this process by accident.
	return fmt.Errorf(
		"refusing to listen on %q: the Admin API is unauthenticated, so this process "+
			"may only bind a loopback address such as 127.0.0.1:8080, localhost:8080 or [::1]:8080",
		addr,
	)
}

// demoAuthenticator builds the single-key chat credential of the local demo. It
// grants exactly one tenant, one principal and one agent app; chat identity is
// never taken from a request. Admin endpoints stay unauthenticated, which is
// why validateListenAddr refuses to bind anything but loopback.
func demoAuthenticator() (*identity.StaticAPIKeyAuthenticator, error) {
	apiKey := os.Getenv(apiKeyEnvVar)
	if apiKey == "" {
		apiKey = developmentAPIKey
		log.Printf(
			"%s is not set; serving chat with the published local development key",
			apiKeyEnvVar,
		)
	}
	return identity.NewStaticAPIKeyAuthenticator(map[string]identity.Identity{
		apiKey: {
			TenantID:      platformconfig.DemoTenantID,
			PrincipalID:   platformconfig.DemoPrincipalID,
			AllowedAppIDs: []string{platformconfig.DemoAgentAppID},
		},
	})
}

type httpServerLifecycle interface {
	Shutdown(context.Context) error
	Close() error
}

// waitForStop blocks until the process is signalled or the server stops serving.
// Both paths must leave no connection open: an active SSE response still holds a
// runtime lease, and RuntimeResolver.Close waits for every lease to be released.
func waitForStop(
	signalCtx context.Context,
	serveErrCh <-chan error,
	server httpServerLifecycle,
	timeout time.Duration,
) error {
	select {
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return shutdownHTTPServer(shutdownCtx, server)
	case err := <-serveErrCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.Join(err, server.Close())
	}
}

// shutdownHTTPServer drains in-flight requests and forces the remaining
// connections closed when the graceful deadline expires or Shutdown fails.
func shutdownHTTPServer(ctx context.Context, server httpServerLifecycle) error {
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(err, server.Close())
	}
	return nil
}
