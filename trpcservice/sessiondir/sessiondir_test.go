package sessiondir

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/stretchr/testify/require"
)

func testKey() Key {
	return Key{
		TenantID:    "tenant-a",
		AppID:       "assistant",
		PrincipalID: "principal-1",
		SessionID:   "conversation-1",
	}
}

func TestMemoryDirectoryPinsFirstCandidate(t *testing.T) {
	directory := NewMemoryDirectory()
	ctx := context.Background()
	key := testKey()

	pinned, found, err := directory.GetPin(ctx, key)
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, pinned)

	winner, err := directory.EnsurePin(ctx, key, "revision-1")
	require.NoError(t, err)
	require.Equal(t, "revision-1", winner)

	// A later run of the same session keeps the original revision even when it
	// arrives with a newer candidate.
	winner, err = directory.EnsurePin(ctx, key, "revision-2")
	require.NoError(t, err)
	require.Equal(t, "revision-1", winner)

	pinned, found, err = directory.GetPin(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "revision-1", pinned)
	require.Equal(t, 1, directory.Size())
}

// Concurrent first runs each carry their own candidate. Exactly one of them may
// become the pin, and every caller has to be told which one.
func TestMemoryDirectoryEnsurePinAgreesOnOneWinner(t *testing.T) {
	directory := NewMemoryDirectory()
	key := testKey()

	const callers = 32
	ready := make(chan struct{}, callers)
	start := make(chan struct{})
	winners := make(chan string, callers)
	failures := make(chan error, callers)
	var waitGroup sync.WaitGroup
	for caller := 0; caller < callers; caller++ {
		candidate := fmt.Sprintf("revision-%d", caller)
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			ready <- struct{}{}
			<-start
			winner, err := directory.EnsurePin(context.Background(), key, candidate)
			if err != nil {
				failures <- err
				return
			}
			winners <- winner
		}()
	}
	for caller := 0; caller < callers; caller++ {
		<-ready
	}
	close(start)
	waitGroup.Wait()
	close(winners)
	close(failures)

	for err := range failures {
		require.NoError(t, err)
	}
	pinned, found, err := directory.GetPin(context.Background(), key)
	require.NoError(t, err)
	require.True(t, found)
	agreed := 0
	for winner := range winners {
		require.Equal(t, pinned, winner)
		agreed++
	}
	require.Equal(t, callers, agreed)
	require.Equal(t, 1, directory.Size())
}

// The same session id under a different tenant, app, principal or epoch is a
// different conversation and must not inherit a pin.
func TestMemoryDirectoryIsolatesKeyFields(t *testing.T) {
	directory := NewMemoryDirectory()
	ctx := context.Background()
	base := testKey()
	winner, err := directory.EnsurePin(ctx, base, "revision-1")
	require.NoError(t, err)
	require.Equal(t, "revision-1", winner)

	for name, mutate := range map[string]func(*Key){
		"tenant":    func(k *Key) { k.TenantID = "tenant-b" },
		"app":       func(k *Key) { k.AppID = "reporter" },
		"principal": func(k *Key) { k.PrincipalID = "principal-2" },
		"epoch":     func(k *Key) { k.Epoch = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			other := base
			mutate(&other)
			pinned, found, pinErr := directory.GetPin(ctx, other)
			require.NoError(t, pinErr)
			require.False(t, found)
			require.Empty(t, pinned)

			pinned, pinErr = directory.EnsurePin(ctx, other, "revision-2")
			require.NoError(t, pinErr)
			require.Equal(t, "revision-2", pinned)
		})
	}

	pinned, found, err := directory.GetPin(ctx, base)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "revision-1", pinned)
	require.Equal(t, 5, directory.Size())
}

func TestMemoryDirectoryRejectsInvalidInput(t *testing.T) {
	directory := NewMemoryDirectory()
	ctx := context.Background()

	for name, mutate := range map[string]func(*Key){
		"tenant":  func(k *Key) { k.TenantID = "" },
		"app":     func(k *Key) { k.AppID = "" },
		"princip": func(k *Key) { k.PrincipalID = "not a principal" },
		"session": func(k *Key) { k.SessionID = "conversation/1" },
	} {
		t.Run(name, func(t *testing.T) {
			key := testKey()
			mutate(&key)
			_, _, err := directory.GetPin(ctx, key)
			require.ErrorIs(t, err, tenant.ErrInvalidArgument)
			_, err = directory.EnsurePin(ctx, key, "revision-1")
			require.ErrorIs(t, err, tenant.ErrInvalidArgument)
		})
	}

	_, err := directory.EnsurePin(ctx, testKey(), "")
	require.ErrorIs(t, err, tenant.ErrInvalidArgument)
	_, err = directory.EnsurePin(ctx, testKey(), "revision 1")
	require.ErrorIs(t, err, tenant.ErrInvalidArgument)
	require.Zero(t, directory.Size())
}

func TestMemoryDirectoryRejectsUnusableContext(t *testing.T) {
	directory := NewMemoryDirectory()

	var missingContext context.Context
	_, _, err := directory.GetPin(missingContext, testKey())
	require.ErrorIs(t, err, tenant.ErrInvalidArgument)
	_, err = directory.EnsurePin(missingContext, testKey(), "revision-1")
	require.ErrorIs(t, err, tenant.ErrInvalidArgument)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = directory.GetPin(cancelled, testKey())
	require.ErrorIs(t, err, context.Canceled)
	_, err = directory.EnsurePin(cancelled, testKey(), "revision-1")
	require.ErrorIs(t, err, context.Canceled)

	var uninitialised *MemoryDirectory
	_, _, err = uninitialised.GetPin(context.Background(), testKey())
	require.ErrorIs(t, err, tenant.ErrInvalidArgument)
	_, err = uninitialised.EnsurePin(context.Background(), testKey(), "revision-1")
	require.ErrorIs(t, err, tenant.ErrInvalidArgument)
	require.Zero(t, uninitialised.Size())
	require.Zero(t, directory.Size())
}
