/*
Copyright The ORAS Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package syncutil

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"
)

type talentsErrorA struct{}

func (talentsErrorA) Error() string { return "talents error a" }

type talentsErrorB struct{}

func (talentsErrorB) Error() string { return "talents error b" }

func TestTalentsGoWaitsForStartedWorkersAndReturnsCause(t *testing.T) {
	limiter := semaphore.NewWeighted(2)
	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	callbackFailed := make(chan struct{})
	errBoom := errors.New("primary callback failure")
	var itemThreeCalls atomic.Int32

	done := make(chan error, 1)
	go func() {
		done <- Go(context.Background(), limiter, func(ctx context.Context, region *LimitedRegion, item int) error {
			switch item {
			case 1:
				<-blockerStarted
				close(callbackFailed)
				return errBoom
			case 2:
				close(blockerStarted)
				<-releaseBlocker // deliberately ignores cancellation until released
				return nil
			case 3:
				itemThreeCalls.Add(1)
				return nil
			default:
				return errors.New("unexpected item")
			}
		}, 1, 2, 3)
	}()

	select {
	case <-callbackFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("failing callback did not run")
	}

	select {
	case err := <-done:
		t.Fatalf("Go returned before an already-started worker stopped: %v", err)
	case <-time.After(80 * time.Millisecond):
	}

	close(releaseBlocker)
	select {
	case err := <-done:
		if !errors.Is(err, errBoom) {
			t.Fatalf("Go error = %v, want causal callback error %v", err, errBoom)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Go did not join the already-started worker")
	}

	if got := itemThreeCalls.Load(); got != 0 {
		t.Fatalf("callback for unscheduled item ran %d times, want 0", got)
	}
	if !limiter.TryAcquire(2) {
		t.Fatal("Go returned with semaphore permits still held")
	}
	limiter.Release(2)
}

func TestTalentsGoPreservesParentCancellationCause(t *testing.T) {
	cause := errors.New("maintenance window closed")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	var calls atomic.Int32

	err := Go(ctx, semaphore.NewWeighted(1), func(ctx context.Context, region *LimitedRegion, item int) error {
		calls.Add(1)
		return nil
	}, 1, 2)
	if !errors.Is(err, cause) {
		t.Fatalf("Go error = %v, want parent cancellation cause %v", err, cause)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("callbacks ran after pre-cancellation: %d", got)
	}
}

func TestTalentsGoHonorsLimitAndRunsEachItemOnce(t *testing.T) {
	const (
		limit = 3
		items = 48
	)
	limiter := semaphore.NewWeighted(limit)
	seen := make([]int, items)
	var seenMu sync.Mutex
	var active atomic.Int32
	var maximum atomic.Int32

	err := Go(context.Background(), limiter, func(ctx context.Context, region *LimitedRegion, item int) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		seenMu.Lock()
		seen[item]++
		seenMu.Unlock()
		time.Sleep(time.Millisecond)
		return nil
	}, func() []int {
		out := make([]int, items)
		for i := range out {
			out[i] = i
		}
		return out
	}()...)
	if err != nil {
		t.Fatal("Go error =", err)
	}
	if got := maximum.Load(); got > limit {
		t.Fatalf("maximum concurrency = %d, want <= %d", got, limit)
	}
	for item, count := range seen {
		if count != 1 {
			t.Fatalf("item %d ran %d times, want exactly once", item, count)
		}
	}
}

func TestTalentsGoDifferentConcreteErrorsDoNotPanic(t *testing.T) {
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)

	done := make(chan error, 1)
	go func() {
		done <- Go(context.Background(), semaphore.NewWeighted(2), func(ctx context.Context, region *LimitedRegion, item int) error {
			ready.Done()
			<-start
			if item == 1 {
				return talentsErrorA{}
			}
			return talentsErrorB{}
		}, 1, 2)
	}()
	ready.Wait()
	close(start)

	select {
	case err := <-done:
		var a talentsErrorA
		var b talentsErrorB
		if !errors.As(err, &a) && !errors.As(err, &b) {
			t.Fatalf("Go error = %v, want one callback error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Go did not return")
	}
}
