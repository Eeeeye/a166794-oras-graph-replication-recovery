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

package content_test

import (
	"bytes"
	"context"
	_ "crypto/sha256"
	"errors"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
)

func talentsCallWithoutPanic(fn func() error) (err error, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	err = fn()
	return
}

func TestTalentsStoresRejectBadDigestsWithoutPublishing(t *testing.T) {
	body := []byte("content that must not be published")
	tests := []struct {
		name string
		dgst digest.Digest
		want error
	}{
		{name: "malformed", dgst: "invalid-digest", want: digest.ErrDigestInvalidFormat},
		{name: "unsupported algorithm", dgst: "sha1:0ff30941ca5acd879fd809e8c937d9f9e6dd1615", want: digest.ErrDigestUnsupported},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc := ocispec.Descriptor{
				MediaType: "application/vnd.example.payload",
				Digest:    tt.dgst,
				Size:      int64(len(body)),
			}
			ctx := context.Background()

			mem := memory.New()
			err, panicValue := talentsCallWithoutPanic(func() error {
				return mem.Push(ctx, desc, bytes.NewReader(body))
			})
			if panicValue != nil {
				t.Fatalf("memory Push panic = %v", panicValue)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("memory Push error = %v, want %v", err, tt.want)
			}
			if exists, err := mem.Exists(ctx, desc); err != nil || exists {
				t.Fatalf("memory store published rejected content: exists=%v err=%v", exists, err)
			}

			fs, err := file.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer fs.Close()
			err, panicValue = talentsCallWithoutPanic(func() error {
				return fs.Push(ctx, desc, bytes.NewReader(body))
			})
			if panicValue != nil {
				t.Fatalf("file Push panic = %v", panicValue)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("file Push error = %v, want %v", err, tt.want)
			}
			if exists, err := fs.Exists(ctx, desc); err != nil || exists {
				t.Fatalf("file store published rejected content: exists=%v err=%v", exists, err)
			}

			layout, err := oci.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			err, panicValue = talentsCallWithoutPanic(func() error {
				return layout.Push(ctx, desc, bytes.NewReader(body))
			})
			if panicValue != nil {
				t.Fatalf("OCI Push panic = %v", panicValue)
			}
			if !errors.Is(err, errdef.ErrInvalidDigest) {
				t.Fatalf("OCI Push error = %v, want %v", err, errdef.ErrInvalidDigest)
			}
			if _, err := layout.Exists(ctx, desc); !errors.Is(err, errdef.ErrInvalidDigest) {
				t.Fatalf("OCI Exists error = %v, want %v", err, errdef.ErrInvalidDigest)
			}
		})
	}
}
