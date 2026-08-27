package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShutdownHTTPServerGraceful(t *testing.T) {
	server := &fakeHTTPServerLifecycle{}

	require.NoError(t, shutdownHTTPServer(context.Background(), server))
	require.Equal(t, 1, server.shutdownCalls)
	require.Zero(t, server.closeCalls)
}

func TestShutdownHTTPServerForcesCloseAfterShutdownFailure(t *testing.T) {
	shutdownErr := context.DeadlineExceeded
	closeErr := errors.New("forced close failure")
	server := &fakeHTTPServerLifecycle{
		shutdownErr: shutdownErr,
		closeErr:    closeErr,
	}

	err := shutdownHTTPServer(context.Background(), server)
	require.ErrorIs(t, err, shutdownErr)
	require.ErrorIs(t, err, closeErr)
	require.Equal(t, 1, server.shutdownCalls)
	require.Equal(t, 1, server.closeCalls)
}

func TestWaitForStopShutsDownOnSignal(t *testing.T) {
	server := &fakeHTTPServerLifecycle{}
	signalCtx, stop := context.WithCancel(context.Background())
	stop()

	require.NoError(t, waitForStop(signalCtx, make(chan error), server, time.Second))
	require.Equal(t, 1, server.shutdownCalls)
	require.Zero(t, server.closeCalls)
}

func TestWaitForStopForcesCloseWhenGracefulShutdownExpires(t *testing.T) {
	server := &fakeHTTPServerLifecycle{shutdownErr: context.DeadlineExceeded}
	signalCtx, stop := context.WithCancel(context.Background())
	stop()

	err := waitForStop(signalCtx, make(chan error), server, time.Second)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, server.shutdownCalls)
	require.Equal(t, 1, server.closeCalls)
}

func TestWaitForStopForcesCloseAfterServeFailure(t *testing.T) {
	serveErr := errors.New("listen failure")
	server := &fakeHTTPServerLifecycle{}
	serveErrCh := make(chan error, 1)
	serveErrCh <- serveErr

	err := waitForStop(context.Background(), serveErrCh, server, time.Second)
	require.ErrorIs(t, err, serveErr)
	require.Zero(t, server.shutdownCalls)
	require.Equal(t, 1, server.closeCalls)
}

func TestWaitForStopIgnoresServerClosed(t *testing.T) {
	server := &fakeHTTPServerLifecycle{}
	serveErrCh := make(chan error, 1)
	serveErrCh <- http.ErrServerClosed

	require.NoError(t, waitForStop(context.Background(), serveErrCh, server, time.Second))
	require.Zero(t, server.shutdownCalls)
	require.Zero(t, server.closeCalls)
}

type fakeHTTPServerLifecycle struct {
	shutdownErr   error
	closeErr      error
	shutdownCalls int
	closeCalls    int
}

func (s *fakeHTTPServerLifecycle) Shutdown(context.Context) error {
	s.shutdownCalls++
	return s.shutdownErr
}

func (s *fakeHTTPServerLifecycle) Close() error {
	s.closeCalls++
	return s.closeErr
}
