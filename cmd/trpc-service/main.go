package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	platformconfig "github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/liuzengh/trpc-agent-service/trpcservice/web"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const shutdownTimeout = 10 * time.Second

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

	api, err := web.NewPlatformServer(repository, resolver)
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
