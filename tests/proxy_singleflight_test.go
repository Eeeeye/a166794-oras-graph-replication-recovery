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

package cas

import (
	"bytes"
	"context"
	_ "crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
)

type talentsProxyBase struct {
	mu      sync.Mutex
	body    []byte
	readErr error
	calls   int
}

func (s *talentsProxyBase) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	s.mu.Lock()
	s.calls++
	body := bytes.Clone(s.body)
	readErr := s.readErr
	s.mu.Unlock()
	if readErr != nil {
		return &talentsProxyFailReader{prefix: body[:len(body)/2], err: readErr}, nil
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (s *talentsProxyBase) Exists(context.Context, ocispec.Descriptor) (bool, error) {
	return true, nil
}

func (s *talentsProxyBase) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *talentsProxyBase) setReadError(err error) {
	s.mu.Lock()
	s.readErr = err
	s.mu.Unlock()
}

type talentsProxyFailReader struct {
	prefix []byte
	err    error
	sent   bool
}

func (r *talentsProxyFailReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.prefix), nil
	}
	return 0, r.err
}

func (r *talentsProxyFailReader) Close() error { return nil }

type talentsObservedCache struct {
	content.Storage
	fetches atomic.Int32
}

func (c *talentsObservedCache) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	c.fetches.Add(1)
	return c.Storage.Fetch(ctx, target)
}

func talentsWaitFetches(t *testing.T, cache *talentsObservedCache, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for cache.fetches.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("cache Fetch calls = %d, want at least %d", cache.fetches.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func talentsReadProxy(rc io.ReadCloser) ([]byte, error) {
	body, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	return body, errors.Join(readErr, closeErr)
}

func TestTalentsProxySingleFlightsConcurrentDigestMisses(t *testing.T) {
	body := bytes.Repeat([]byte("single-flight payload/"), 32768)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)
	base := &talentsProxyBase{body: body}
	proxy := NewProxy(base, NewMemory())

	const callers = 32
	start := make(chan struct{})
	errCh := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			alias := desc
			alias.Annotations = map[string]string{"caller": fmt.Sprint(i)}
			rc, err := proxy.Fetch(context.Background(), alias)
			if err != nil {
				errCh <- err
				return
			}
			got, err := talentsReadProxy(rc)
			if err != nil {
				errCh <- err
				return
			}
			if !bytes.Equal(got, body) {
				errCh <- fmt.Errorf("caller %d received %d changed bytes", i, len(got))
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if got := base.callCount(); got != 1 {
		t.Fatalf("base Fetch calls = %d, want exactly 1", got)
	}
}

func TestTalentsProxyFailureReleasesFollowersAndAllowsRetry(t *testing.T) {
	body := []byte("content that fails during the first remote stream")
	wantErr := errors.New("remote stream interrupted")
	base := &talentsProxyBase{body: body, readErr: wantErr}
	cache := &talentsObservedCache{Storage: NewMemory()}
	proxy := NewProxy(base, cache)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)

	leader, err := proxy.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	followerDone := make(chan error, 1)
	go func() {
		_, err := proxy.Fetch(context.Background(), desc)
		followerDone <- err
	}()
	talentsWaitFetches(t, cache, 2)
	time.Sleep(5 * time.Millisecond) // let the follower enter the flight wait
	_, leaderErr := talentsReadProxy(leader)
	if !errors.Is(leaderErr, wantErr) {
		t.Fatalf("leader error = %v, want causal %v", leaderErr, wantErr)
	}
	select {
	case followerErr := <-followerDone:
		if !errors.Is(followerErr, wantErr) {
			t.Fatalf("follower error = %v, want causal %v", followerErr, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower remained blocked after leader failure")
	}

	base.setReadError(nil)
	rc, err := proxy.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatalf("retry Fetch() error = %v", err)
	}
	got, err := talentsReadProxy(rc)
	if err != nil {
		t.Fatalf("retry stream error = %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("retry body = %q, want %q", got, body)
	}
	if got := base.callCount(); got != 2 {
		t.Fatalf("base Fetch calls after retry = %d, want 2", got)
	}
}

func TestTalentsProxyShortCloseReleasesFlightAndAllowsRetry(t *testing.T) {
	body := bytes.Repeat([]byte("must be fully verified"), 512)
	base := &talentsProxyBase{body: body}
	cache := &talentsObservedCache{Storage: NewMemory()}
	proxy := NewProxy(base, cache)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)
	leader, err := proxy.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	followerDone := make(chan error, 1)
	go func() {
		_, err := proxy.Fetch(context.Background(), desc)
		followerDone <- err
	}()
	talentsWaitFetches(t, cache, 2)
	if err := leader.Close(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short Close() error = %v, want %v", err, io.ErrUnexpectedEOF)
	}
	select {
	case err := <-followerDone:
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("short-close follower error = %v, want %v", err, io.ErrUnexpectedEOF)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower remained blocked after short close")
	}
	rc, err := proxy.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatalf("retry Fetch() error = %v", err)
	}
	got, err := talentsReadProxy(rc)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("retry after short close: len=%d err=%v", len(got), err)
	}
	if got := base.callCount(); got != 2 {
		t.Fatalf("base calls after short-close retry = %d, want 2", got)
	}
}

func TestTalentsProxyFollowerCancellationDoesNotPoisonFlight(t *testing.T) {
	body := bytes.Repeat([]byte("surviving leader"), 1024)
	base := &talentsProxyBase{body: body}
	cache := &talentsObservedCache{Storage: NewMemory()}
	proxy := NewProxy(base, cache)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)
	leader, err := proxy.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}

	cause := errors.New("follower no longer needs content")
	ctx, cancel := context.WithCancelCause(context.Background())
	followerDone := make(chan error, 1)
	go func() {
		_, err := proxy.Fetch(ctx, desc)
		followerDone <- err
	}()
	talentsWaitFetches(t, cache, 2)
	cancel(cause)
	select {
	case err := <-followerDone:
		if !errors.Is(err, cause) {
			t.Fatalf("canceled follower error = %v, want cause %v", err, cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled follower remained blocked")
	}

	got, err := talentsReadProxy(leader)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("leader after follower cancel: len=%d err=%v", len(got), err)
	}
	rc, err := proxy.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	got, err = talentsReadProxy(rc)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("cached fetch after follower cancel: len=%d err=%v", len(got), err)
	}
	if got := base.callCount(); got != 1 {
		t.Fatalf("follower cancellation caused %d base calls, want 1", got)
	}
}

type talentsFailingCache struct{ err error }

func (c *talentsFailingCache) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return nil, c.err
}
func (c *talentsFailingCache) Exists(context.Context, ocispec.Descriptor) (bool, error) {
	return false, c.err
}
func (c *talentsFailingCache) Push(context.Context, ocispec.Descriptor, io.Reader) error {
	return c.err
}

func TestTalentsProxyOnlyNotFoundFallsBackToRemote(t *testing.T) {
	body := []byte("must not be fetched")
	base := &talentsProxyBase{body: body}
	wantErr := errors.New("cache backend unavailable")
	proxy := NewProxy(base, &talentsFailingCache{err: wantErr})
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)
	if _, err := proxy.Fetch(context.Background(), desc); !errors.Is(err, wantErr) {
		t.Fatalf("Fetch() error = %v, want cache error %v", err, wantErr)
	}
	if got := base.callCount(); got != 0 {
		t.Fatalf("cache error caused %d base calls, want 0", got)
	}
}

type talentsRacingCache struct{ *Memory }

func (c *talentsRacingCache) Push(ctx context.Context, target ocispec.Descriptor, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := c.Memory.Push(ctx, target, bytes.NewReader(body)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return err
	}
	return fmt.Errorf("external cache writer won: %w", errdef.ErrAlreadyExists)
}

type talentsFalseAlreadyExistsCache struct{}

func (*talentsFalseAlreadyExistsCache) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return nil, errdef.ErrNotFound
}
func (*talentsFalseAlreadyExistsCache) Exists(context.Context, ocispec.Descriptor) (bool, error) {
	return false, nil
}
func (*talentsFalseAlreadyExistsCache) Push(context.Context, ocispec.Descriptor, io.Reader) error {
	return errdef.ErrAlreadyExists
}

func TestTalentsProxyTreatsFetchableAlreadyExistsAsSuccess(t *testing.T) {
	body := []byte("externally committed cache fill")
	base := &talentsProxyBase{body: body}
	cache := &talentsRacingCache{Memory: NewMemory()}
	proxy := NewProxy(base, cache)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)
	rc, err := proxy.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := talentsReadProxy(rc)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("raced fill stream: len=%d err=%v", len(got), err)
	}
	rc, err = cache.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatalf("raced cache content is not fetchable: %v", err)
	}
	_ = rc.Close()
}

func TestTalentsProxyRejectsUnfetchableAlreadyExists(t *testing.T) {
	body := []byte("cache never actually committed this")
	base := &talentsProxyBase{body: body}
	proxy := NewProxy(base, &talentsFalseAlreadyExistsCache{})
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)
	rc, err := proxy.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	_, err = talentsReadProxy(rc)
	if !errors.Is(err, errdef.ErrAlreadyExists) || !errors.Is(err, errdef.ErrNotFound) {
		t.Fatalf("unfetchable AlreadyExists error = %v, want both cache causes", err)
	}
}
