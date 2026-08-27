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

package remote_test

import (
	"context"
	_ "crypto/sha256"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
)

type talentsCountingClient struct {
	calls atomic.Int32
}

func (c *talentsCountingClient) Do(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("unexpected HTTP request")
}

func TestTalentsReferrerFallbackValidatesDigestBeforeNetwork(t *testing.T) {
	tests := []struct {
		name string
		dgst digest.Digest
		want error
	}{
		{name: "malformed", dgst: "invalid-digest", want: digest.ErrDigestInvalidFormat},
		{name: "unsupported", dgst: "sha1:0ff30941ca5acd879fd809e8c937d9f9e6dd1615", want: digest.ErrDigestUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &talentsCountingClient{}
			repo, err := remote.NewRepository("registry.example.com/team/project")
			if err != nil {
				t.Fatal(err)
			}
			repo.Client = client
			if err := repo.SetReferrersCapability(false); err != nil {
				t.Fatal(err)
			}
			desc := ocispec.Descriptor{Digest: tt.dgst, Size: 1}

			var gotErr error
			var panicValue any
			func() {
				defer func() { panicValue = recover() }()
				gotErr = repo.Referrers(context.Background(), desc, "", func([]ocispec.Descriptor) error {
					t.Fatal("callback ran for invalid subject digest")
					return nil
				})
			}()
			if panicValue != nil {
				t.Fatalf("Referrers panic = %v", panicValue)
			}
			if !errors.Is(gotErr, tt.want) {
				t.Fatalf("Referrers error = %v, want %v", gotErr, tt.want)
			}
			if got := client.calls.Load(); got != 0 {
				t.Fatalf("invalid digest caused %d HTTP requests, want 0", got)
			}
		})
	}
}
