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
	"github.com/liuzengh/trpc-agent-service/trpcservice/security"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tool"
	"github.com/liuzengh/trpc-agent-service/trpcservice/web"
)

const shutdownTimeout = 10 * time.Second

// startupTimeout bounds everything between the first connection attempt and a
// serving process. It has to allow for a migration waiting on the advisory lock
// another booting worker is holding, and it exists so an unreachable database
// fails the process instead of hanging it. The inmemory profile never reaches
// it, having nothing to wait for.
const startupTimeout = 30 * time.Second

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

// run starts the process and returns when it has stopped serving and released
// everything it owns.
//
// The error is named because shutdown contributes to it. Closing a session
// store or a connection pool can fail, and those failures used to be logged and
// dropped; joined into the result they reach the exit status, where a
// supervisor can see them.
//
// The order below is the whole lifecycle, and each step is placed where it is
// on purpose:
//
//   - The listen address is checked first. It costs nothing and it is the guard
//     that keeps the control plane off the network, so it must not sit behind
//     anything that can connect to a database.
//   - The security configuration is loaded, resolved and cross-validated in
//     full before anything is opened. It reads a file and the environment and
//     touches nothing else, so it is the cheapest possible refusal — and a
//     process whose credentials are misconfigured must not have connected to a
//     shared database or run a migration on the way to finding that out.
//   - The storage configuration is loaded and validated as a whole, before a
//     single resource is opened, so a typo in a schema name is a refusal rather
//     than a half-built process.
//   - Cleanup is registered by deferring, which makes the shutdown order the
//     reverse of the startup order for free: HTTP drain inside waitForStop,
//     then the resolver's leases, then the session store, then the shared pool.
//     The resolver waits for in-flight runtimes, and a runtime still writing to
//     a session store that had already been closed would lose the last turn of
//     the conversation it was serving.
func run(addr string) error {
	return runWith(addr, os.Getenv, defaultStorageDeps())
}

// runWith is run with its two sources of outside state made explicit: the
// environment it reads, and the constructors that actually touch a database.
//
// The seam exists so the startup *order* can be tested rather than merely
// documented. Given an environment that is wrong in two ways at once, a test can
// assert which refusal comes back and that no storage constructor was reached —
// which is the only way to keep "security first" from decaying into "security
// eventually" as steps are added above it.
func runWith(addr string, getenv func(string) string, deps storageDeps) (err error) {
	if err := validateListenAddr(addr); err != nil {
		return err
	}

	// Before storage, and before any deadline: the tool registry is the same
	// static one every Runtime resolves against, so a policy this manifest
	// entitles is checked against the policies that actually exist.
	securityCfg, err := security.Load(getenv, tool.Builtin())
	if err != nil {
		return err
	}
	// Safe to log: Description names configuration sources and counts, never a
	// key, a hash or a resolved value.
	log.Printf("security %s", securityCfg.Description)

	storageCfg, err := loadStorageConfig(getenv)
	if err != nil {
		return err
	}
	// Safe to log: describe reports presence, never contents.
	log.Printf("storage %s", storageCfg.describe())

	// One deadline over every connection, migration and constructor between
	// here and a serving process.
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), startupTimeout)
	defer cancelStartup()

	stack, err := openStorage(startupCtx, storageCfg, deps)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stack.close()) }()

	if err := platformconfig.SeedDemo(startupCtx, stack.repository); err != nil {
		return err
	}

	resolver, err := platformagent.NewRuntimeResolver(
		stack.repository,
		func(
			_ context.Context,
			revision tenant.AgentRevision,
		) (*platformagent.Runtime, error) {
			// The same authorizer value the Admin API checks against. One
			// instance, not two equivalent ones: a revision that Admin accepted
			// and a Runtime later refused — or the reverse — would be a
			// disagreement about what this tenant may do, and there is no
			// correct way to resolve one at request time.
			return platformagent.NewRuntimeFromRevision(
				revision, stack.sessions, securityCfg.Revisions)
		},
	)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resolver.Close()) }()

	api, err := web.NewPlatformServer(
		stack.repository,
		resolver,
		securityCfg.Chat,
		securityCfg.Admin,
		securityCfg.Revisions,
		stack.directory,
		stack.coordinator,
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

// validateListenAddr fails closed on every address that is not loopback.
//
// The Admin API now authenticates, so this is no longer the only thing standing
// between the control plane and the network — but it stays, and it stays
// non-overridable. What it defends is everything authentication does not: this
// process serves plain HTTP, so a routable bind would put admin Bearer tokens on
// the wire in cleartext, and the demo profile still boots with a published chat
// key. Exposure is a deployment decision that belongs to a reverse proxy
// terminating TLS, not to an environment variable read by this binary.
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
		"refusing to listen on %q: this process serves plain HTTP and must sit behind a "+
			"TLS-terminating proxy, so it may only bind a loopback address such as "+
			"127.0.0.1:8080, localhost:8080 or [::1]:8080",
		addr,
	)
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
