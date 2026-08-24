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
	"github.com/liuzengh/trpc-agent-service/trpcservice/web"
)

const shutdownTimeout = 10 * time.Second

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
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
	runtime := platformagent.NewDemoRuntime()
	defer func() {
		if err := runtime.Close(); err != nil {
			log.Printf("close agent runtime: %v", err)
		}
	}()

	api, err := web.NewServer(runtime)
	if err != nil {
		return err
	}
	defer func() {
		if err := api.Close(); err != nil {
			log.Printf("close HTTP adapter: %v", err)
		}
	}()

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf(
			"trpc-agent-service %s listening on %s; endpoint=/v1/chat/completions",
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

	select {
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
