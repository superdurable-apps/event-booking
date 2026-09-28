//go:build integration

package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/superdurable/dex/blob-cache-go/blobcache"
	"github.com/superdurable/dex/sdk-go/dex"
)

func TestCapacityFlowNeverOversellsConcurrentReservations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	registry, err := dex.NewRegistry([]dex.Flow{EventCapacityFlow})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	cache, err := blobcache.New(&blobcache.Config{Dir: t.TempDir(), MaxBytes: 64 << 20})
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	worker, err := dex.NewWorker(registry, cache, dex.WorkerOptions{
		BindAddress: os.Getenv("DEX_WORKER_TARGET"), WorkerTarget: dex.WorkerTarget{Address: os.Getenv("DEX_WORKER_TARGET")},
		FlowServiceAddress: os.Getenv("DEX_FLOW_SERVICE_ADDRESS"), Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	client, err := dex.NewClient(registry, cache, dex.ClientOptions{
		FlowServiceAddress: os.Getenv("DEX_FLOW_SERVICE_ADDRESS"), WorkerTarget: worker.WorkerTarget(), Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = worker.Stop(stopCtx)
		_ = client.Close()
		_ = cache.Close()
	}()
	workerResult := make(chan error, 1)
	go func() { workerResult <- worker.Start() }()
	config := EventConfig{ID: "capacity-integration", Capacity: 300}
	capacity := NewCapacityClient(config)
	capacity.Attach(client)
	if err := capacity.Start(ctx); err != nil {
		t.Fatalf("start capacity Flow: %v", err)
	}
	waitForCapacity(t, ctx, capacity)

	var accepted atomic.Int64
	var wait sync.WaitGroup
	errorsFound := make(chan error, 350)
	for index := range 350 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			registrationID := fmt.Sprintf("registration-%03d", index)
			for attempt := 0; ; attempt++ {
				result, reserveErr := capacity.Reserve(ctx, registrationID)
				if reserveErr == nil {
					if result.Accepted {
						accepted.Add(1)
					}
					return
				}
				if ctx.Err() != nil || attempt >= 200 {
					errorsFound <- reserveErr
					return
				}
				time.Sleep(time.Duration(2+index%7) * time.Millisecond)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for reserveErr := range errorsFound {
		t.Errorf("reserve: %v", reserveErr)
	}
	if got := accepted.Load(); got != 300 {
		t.Fatalf("accepted reservations = %d, want 300", got)
	}
	snapshot, err := capacity.Get(ctx)
	if err != nil {
		t.Fatalf("capacity snapshot: %v", err)
	}
	if snapshot.ReservedCount != 300 {
		t.Fatalf("reserved count = %d, want 300", snapshot.ReservedCount)
	}
	select {
	case err := <-workerResult:
		if err != nil {
			t.Fatalf("worker stopped: %v", err)
		}
	default:
	}
}

func waitForCapacity(t *testing.T, ctx context.Context, capacity *CapacityClient) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if snapshot, err := capacity.Get(ctx); err == nil && snapshot.Capacity == 300 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for capacity Flow: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}
