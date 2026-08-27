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
	"bytes"
	"context"
	_ "crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/internal/cas"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

type talentsGraphFixture struct {
	src       content.Storage
	root      ocispec.Descriptor
	manifests []ocispec.Descriptor
	blobs     []ocispec.Descriptor
}

func talentsBuildGraph(t *testing.T) talentsGraphFixture {
	t.Helper()
	ctx := context.Background()
	src := cas.NewMemory()
	contents := make(map[digest.Digest][]byte)

	makeBlob := func(mediaType string, body []byte) ocispec.Descriptor {
		desc := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(body),
			Size:      int64(len(body)),
		}
		contents[desc.Digest] = body
		return desc
	}
	makeManifest := func(config ocispec.Descriptor, layer ocispec.Descriptor) ocispec.Descriptor {
		body, err := json.Marshal(ocispec.Manifest{
			MediaType: ocispec.MediaTypeImageManifest,
			Config:    config,
			Layers:    []ocispec.Descriptor{layer},
		})
		if err != nil {
			t.Fatal(err)
		}
		return makeBlob(ocispec.MediaTypeImageManifest, body)
	}

	cfgA := makeBlob(ocispec.MediaTypeImageConfig, []byte("config-linux-amd64"))
	layerA := makeBlob(ocispec.MediaTypeImageLayer, []byte("layer-linux-amd64"))
	cfgB := makeBlob(ocispec.MediaTypeImageConfig, []byte("config-linux-arm64"))
	layerB := makeBlob(ocispec.MediaTypeImageLayer, []byte("layer-linux-arm64"))
	manifestA := makeManifest(cfgA, layerA)
	manifestB := makeManifest(cfgB, layerB)
	indexBody, err := json.Marshal(ocispec.Index{
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{manifestA, manifestB},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := makeBlob(ocispec.MediaTypeImageIndex, indexBody)

	ordered := []ocispec.Descriptor{cfgA, layerA, cfgB, layerB, manifestA, manifestB, root}
	for _, desc := range ordered {
		if err := src.Push(ctx, desc, bytes.NewReader(contents[desc.Digest])); err != nil {
			t.Fatalf("seed source %s: %v", desc.Digest, err)
		}
	}
	return talentsGraphFixture{
		src:       src,
		root:      root,
		manifests: []ocispec.Descriptor{manifestA, manifestB},
		blobs:     []ocispec.Descriptor{cfgA, layerA, cfgB, layerB},
	}
}

func talentsSeed(t *testing.T, src content.Storage, dst content.Storage, descs ...ocispec.Descriptor) {
	t.Helper()
	ctx := context.Background()
	for _, desc := range descs {
		body, err := content.FetchAll(ctx, src, desc)
		if err != nil {
			t.Fatal(err)
		}
		if err := dst.Push(ctx, desc, bytes.NewReader(body)); err != nil {
			t.Fatalf("seed destination %s: %v", desc.Digest, err)
		}
	}
}

type talentsInterruptedDestination struct {
	content.Storage
	mu         sync.Mutex
	phantom    map[digest.Digest]bool
	committed  map[digest.Digest]bool
	pushes     map[digest.Digest]int
	persistent bool
}

func talentsNewInterruptedDestination(storage content.Storage, persistent bool, phantom ...ocispec.Descriptor) *talentsInterruptedDestination {
	dst := &talentsInterruptedDestination{
		Storage:    storage,
		phantom:    make(map[digest.Digest]bool),
		committed:  make(map[digest.Digest]bool),
		pushes:     make(map[digest.Digest]int),
		persistent: persistent,
	}
	for _, desc := range phantom {
		dst.phantom[desc.Digest] = true
	}
	return dst
}

func (d *talentsInterruptedDestination) Exists(ctx context.Context, desc ocispec.Descriptor) (bool, error) {
	d.mu.Lock()
	phantom := d.phantom[desc.Digest] && !d.committed[desc.Digest]
	d.mu.Unlock()
	if phantom {
		return true, nil
	}
	return d.Storage.Exists(ctx, desc)
}

func (d *talentsInterruptedDestination) Push(ctx context.Context, expected ocispec.Descriptor, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.pushes[expected.Digest]++
	d.mu.Unlock()

	if expected.MediaType == ocispec.MediaTypeImageIndex {
		var index ocispec.Index
		if err := json.Unmarshal(body, &index); err != nil {
			return err
		}
		d.mu.Lock()
		var missing []ocispec.Descriptor
		for _, child := range index.Manifests {
			if d.phantom[child.Digest] && !d.committed[child.Digest] {
				missing = append(missing, child)
			}
		}
		persistent := d.persistent
		d.mu.Unlock()
		if persistent {
			return fmt.Errorf("registry push rejected: %w", errcode.Error{
				Code:    errcode.ErrorCodeManifestUnknown,
				Message: "referenced manifest is not durably committed",
			})
		}
		if len(missing) != 0 {
			errs := errcode.Errors{{Code: errcode.ErrorCodeNameUnknown, Message: "decoy"}}
			for _, child := range missing {
				errs = append(errs, errcode.Error{
					Code:    errcode.ErrorCodeManifestBlobUnknown,
					Message: "referenced manifest missing",
					Detail:  child.Digest.String(),
				})
			}
			return fmt.Errorf("registry push rejected: %w", errs)
		}
	}

	d.mu.Lock()
	if d.phantom[expected.Digest] {
		d.committed[expected.Digest] = true
	}
	d.mu.Unlock()
	return d.Storage.Push(ctx, expected, bytes.NewReader(body))
}

func (d *talentsInterruptedDestination) pushCount(desc ocispec.Descriptor) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pushes[desc.Digest]
}

type talentsRootRejectDestination struct {
	content.Storage
	root   digest.Digest
	err    error
	mu     sync.Mutex
	pushes map[digest.Digest]int
}

func (d *talentsRootRejectDestination) Push(ctx context.Context, expected ocispec.Descriptor, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.pushes[expected.Digest]++
	d.mu.Unlock()
	if expected.Digest == d.root {
		return d.err
	}
	return d.Storage.Push(ctx, expected, bytes.NewReader(body))
}

func (d *talentsRootRejectDestination) pushCount(desc ocispec.Descriptor) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pushes[desc.Digest]
}

func TestTalentsCopyGraphSelfHealsTypedMissingReferences(t *testing.T) {
	fixture := talentsBuildGraph(t)
	backing := cas.NewMemory()
	talentsSeed(t, fixture.src, backing, fixture.blobs...)
	dst := talentsNewInterruptedDestination(backing, false, fixture.manifests...)

	if err := oras.CopyGraph(context.Background(), fixture.src, dst, fixture.root, oras.CopyGraphOptions{Concurrency: 4}); err != nil {
		t.Fatalf("CopyGraph interrupted recovery error = %v", err)
	}
	if got := dst.pushCount(fixture.root); got != 2 {
		t.Fatalf("root push count = %d, want initial push plus one retry", got)
	}
	for _, manifest := range fixture.manifests {
		if got := dst.pushCount(manifest); got != 1 {
			t.Fatalf("manifest %s recovery push count = %d, want 1", manifest.Digest, got)
		}
	}
	for _, blob := range fixture.blobs {
		if got := dst.pushCount(blob); got != 0 {
			t.Fatalf("blob %s was wastefully re-pushed %d times", blob.Digest, got)
		}
	}
	for _, desc := range append(append([]ocispec.Descriptor{}, fixture.manifests...), fixture.root) {
		got, err := content.FetchAll(context.Background(), backing, desc)
		if err != nil {
			t.Fatalf("destination missing %s after recovery: %v", desc.Digest, err)
		}
		want, err := content.FetchAll(context.Background(), fixture.src, desc)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("destination content mismatch for %s", desc.Digest)
		}
	}
}

func TestTalentsCopyGraphRecoveryRetriesParentOnlyOnce(t *testing.T) {
	fixture := talentsBuildGraph(t)
	backing := cas.NewMemory()
	talentsSeed(t, fixture.src, backing, fixture.blobs...)
	dst := talentsNewInterruptedDestination(backing, true, fixture.manifests...)

	err := oras.CopyGraph(context.Background(), fixture.src, dst, fixture.root, oras.CopyGraphOptions{Concurrency: 4})
	if err == nil {
		t.Fatal("CopyGraph persistent rejection error = nil")
	}
	if got := dst.pushCount(fixture.root); got != 2 {
		t.Fatalf("persistent root push count = %d, want exactly 2", got)
	}
	for _, manifest := range fixture.manifests {
		if got := dst.pushCount(manifest); got != 1 {
			t.Fatalf("persistent recovery manifest push count = %d, want 1", got)
		}
	}
	for _, blob := range fixture.blobs {
		if got := dst.pushCount(blob); got != 0 {
			t.Fatalf("persistent recovery re-pushed blob %s %d times", blob.Digest, got)
		}
	}
}

func TestTalentsCopyGraphDoesNotStringMatchOrRetryUnrelatedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "plain text decoy",
			err:  errors.New("MANIFEST_BLOB_UNKNOWN appears only as untrusted text"),
		},
		{
			name: "unrelated typed code",
			err: errcode.Error{
				Code:    errcode.ErrorCodeDenied,
				Message: "destination denied the push",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := talentsBuildGraph(t)
			backing := cas.NewMemory()
			allSuccessors := append(append([]ocispec.Descriptor{}, fixture.blobs...), fixture.manifests...)
			talentsSeed(t, fixture.src, backing, allSuccessors...)
			dst := &talentsRootRejectDestination{
				Storage: backing,
				root:    fixture.root.Digest,
				err:     tt.err,
				pushes:  make(map[digest.Digest]int),
			}
			if err := oras.CopyGraph(context.Background(), fixture.src, dst, fixture.root, oras.CopyGraphOptions{}); err == nil {
				t.Fatal("CopyGraph error = nil")
			}
			if got := dst.pushCount(fixture.root); got != 1 {
				t.Fatalf("root push count = %d, want no recovery retry", got)
			}
			for _, manifest := range fixture.manifests {
				if got := dst.pushCount(manifest); got != 0 {
					t.Fatalf("unrelated error re-pushed manifest %s %d times", manifest.Digest, got)
				}
			}
		})
	}
}
