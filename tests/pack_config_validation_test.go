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

package oras_test

import (
	"context"
	_ "crypto/sha256"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/internal/cas"
)

type talentsCountingPusher struct {
	store *cas.Memory
	calls atomic.Int32
}

func (p *talentsCountingPusher) Push(ctx context.Context, desc ocispec.Descriptor, r io.Reader) error {
	p.calls.Add(1)
	return p.store.Push(ctx, desc, r)
}

type talentsPackPath struct {
	name string
	pack func(context.Context, content.Pusher, *ocispec.Descriptor) error
}

func talentsPackPaths() []talentsPackPath {
	layerBody := []byte("existing layer")
	layer := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, layerBody)
	return []talentsPackPath{
		{
			name: "manifest v1.0",
			pack: func(ctx context.Context, p content.Pusher, config *ocispec.Descriptor) error {
				_, err := oras.PackManifest(ctx, p, oras.PackManifestVersion1_0, "application/vnd.example.artifact", oras.PackManifestOptions{
					ConfigDescriptor: config,
					Layers:           []ocispec.Descriptor{layer},
				})
				return err
			},
		},
		{
			name: "manifest v1.1",
			pack: func(ctx context.Context, p content.Pusher, config *ocispec.Descriptor) error {
				_, err := oras.PackManifest(ctx, p, oras.PackManifestVersion1_1, "application/vnd.example.artifact", oras.PackManifestOptions{
					ConfigDescriptor: config,
					Layers:           []ocispec.Descriptor{layer},
				})
				return err
			},
		},
		{
			name: "deprecated RC2",
			pack: func(ctx context.Context, p content.Pusher, config *ocispec.Descriptor) error {
				_, err := oras.Pack(ctx, p, "application/vnd.example.artifact", []ocispec.Descriptor{layer}, oras.PackOptions{
					PackImageManifest: true,
					ConfigDescriptor:  config,
				})
				return err
			},
		},
	}
}

func TestTalentsPackRejectsInvalidConfigBeforeMutation(t *testing.T) {
	invalid := []struct {
		name string
		desc ocispec.Descriptor
		want error
	}{
		{
			name: "empty digest",
			desc: ocispec.Descriptor{MediaType: "application/vnd.example.config"},
			want: errdef.ErrInvalidDigest,
		},
		{
			name: "malformed digest",
			desc: ocispec.Descriptor{MediaType: "application/vnd.example.config", Digest: digest.Digest("not-a-digest")},
			want: errdef.ErrInvalidDigest,
		},
		{
			name: "unsupported digest",
			desc: ocispec.Descriptor{MediaType: "application/vnd.example.config", Digest: digest.Digest("sha999:001122")},
			want: errdef.ErrInvalidDigest,
		},
		{
			name: "invalid media type",
			desc: ocispec.Descriptor{MediaType: "not a media type", Digest: digest.FromBytes([]byte("config")), Size: 6},
			want: errdef.ErrInvalidMediaType,
		},
	}
	for _, path := range talentsPackPaths() {
		for _, tc := range invalid {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				pusher := &talentsCountingPusher{store: cas.NewMemory()}
				err := path.pack(context.Background(), pusher, &tc.desc)
				if !errors.Is(err, tc.want) {
					t.Fatalf("pack error = %v, want %v", err, tc.want)
				}
				if got := pusher.calls.Load(); got != 0 {
					t.Fatalf("invalid descriptor caused %d pusher mutations, want 0", got)
				}
			})
		}
	}
}

func TestTalentsPackAcceptsValidCallerManagedConfig(t *testing.T) {
	body := []byte("caller-managed config")
	config := content.NewDescriptorFromBytes("application/vnd.example.config", body)
	for _, path := range talentsPackPaths() {
		t.Run(path.name, func(t *testing.T) {
			pusher := &talentsCountingPusher{store: cas.NewMemory()}
			if err := path.pack(context.Background(), pusher, &config); err != nil {
				t.Fatalf("pack error = %v", err)
			}
			if got := pusher.calls.Load(); got != 1 {
				t.Fatalf("pusher calls = %d, want only the manifest mutation", got)
			}
			if exists, err := pusher.store.Exists(context.Background(), config); err != nil || exists {
				t.Fatalf("Pack unexpectedly pushed caller-managed config: exists=%v err=%v", exists, err)
			}
		})
	}
}
