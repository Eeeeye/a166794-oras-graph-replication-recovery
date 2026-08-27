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

package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/internal/cas"
)

func TestTalentsMemoryUsesDigestIdentityAcrossMetadataAliases(t *testing.T) {
	ctx := context.Background()
	store := cas.NewMemory()

	childBody := []byte("shared layer")
	child := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(childBody),
		Size:      int64(len(childBody)),
	}
	manifestBody, err := json.Marshal(ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    child,
		Layers:    []ocispec.Descriptor{child},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestBody),
		Size:      int64(len(manifestBody)),
	}
	if err := store.Push(ctx, child, bytes.NewReader(childBody)); err != nil {
		t.Fatal(err)
	}
	if err := store.Push(ctx, parent, bytes.NewReader(manifestBody)); err != nil {
		t.Fatal(err)
	}

	memory := NewMemory()
	if err := memory.Index(ctx, store, parent); err != nil {
		t.Fatal("Index(parent) error =", err)
	}
	if err := memory.Index(ctx, store, child); err != nil {
		t.Fatal("Index(child) error =", err)
	}

	parentAlias := parent
	parentAlias.MediaType = "application/vnd.example.alias"
	parentAlias.Size += 999
	parentAlias.Annotations = map[string]string{"source": "decoy metadata"}
	childAlias := child
	childAlias.MediaType = "application/octet-stream"
	childAlias.Size = 0
	childAlias.Annotations = map[string]string{"name": "same digest"}

	if !memory.Exists(parentAlias) {
		t.Fatal("Exists(alias) = false for an indexed digest")
	}
	if !memory.Exists(childAlias) {
		t.Fatal("Exists(child alias) = false for an indexed digest")
	}
	if got := len(memory.DigestSet()); got != 2 {
		t.Fatalf("DigestSet length = %d, want 2 digest nodes", got)
	}

	preds, err := memory.Predecessors(ctx, childAlias)
	if err != nil {
		t.Fatal("Predecessors(alias) error =", err)
	}
	if len(preds) != 1 || preds[0].Digest != parent.Digest {
		t.Fatalf("Predecessors(alias) = %#v, want parent digest %s once", preds, parent.Digest)
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = memory.Exists(parentAlias)
				_, _ = memory.Predecessors(ctx, childAlias)
				_ = memory.DigestSet()
			}
		}()
	}
	wg.Wait()

	dangling := memory.Remove(parentAlias)
	if memory.Exists(parent) {
		t.Fatal("Remove(alias) did not remove the digest node")
	}
	if len(dangling) != 1 || dangling[0].Digest != child.Digest {
		t.Fatalf("Remove(alias) dangling = %#v, want child digest %s once", dangling, child.Digest)
	}
	preds, err = memory.Predecessors(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(preds) != 0 {
		t.Fatalf("child retains predecessors after alias removal: %#v", preds)
	}
}
