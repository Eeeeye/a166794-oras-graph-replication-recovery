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

package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/internal/cas"
)

type talentsFallback struct {
	content.Storage
	onFetch func(context.Context, ocispec.Descriptor) error
}

func (m *talentsFallback) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	if m.onFetch != nil {
		if err := m.onFetch(ctx, desc); err != nil {
			return nil, err
		}
	}
	return m.Storage.Fetch(ctx, desc)
}

func talentsManifest(t *testing.T, blobDesc ocispec.Descriptor) ([]byte, ocispec.Descriptor) {
	t.Helper()
	config := []byte("{}")
	configDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromBytes(config),
		Size:      int64(len(config)),
	}
	manifestJSON, err := json.Marshal(ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{blobDesc},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifestJSON, ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestJSON),
		Size:      int64(len(manifestJSON)),
	}
}

func TestTalentsRestoreDuplicateNameRaceIsBenign(t *testing.T) {
	ctx := context.Background()
	body := []byte("verified named layer")
	blob := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(body),
		Size:      int64(len(body)),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: "layers/platform.bin",
		},
	}
	manifestJSON, manifest := talentsManifest(t, blob)
	fallback := &talentsFallback{Storage: cas.NewMemory()}
	if err := fallback.Push(ctx, blob, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := NewWithFallbackStorage(root, fallback)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var injected bool
	fallback.onFetch = func(ctx context.Context, desc ocispec.Descriptor) error {
		if desc.Digest == blob.Digest && !injected {
			injected = true
			if err := store.Push(ctx, blob, bytes.NewReader(body)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := store.Push(ctx, manifest, bytes.NewReader(manifestJSON)); err != nil {
		t.Fatalf("manifest Push failed on benign duplicate restore: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "layers", "platform.bin"))
	if err != nil {
		t.Fatal("restored named file missing:", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("restored named file = %q, want %q", got, body)
	}
}

func TestTalentsRestoreStillPropagatesNonBenignFailure(t *testing.T) {
	ctx := context.Background()
	body := []byte("layer")
	blob := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(body),
		Size:      int64(len(body)),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: "layer.bin",
		},
	}
	manifestJSON, manifest := talentsManifest(t, blob)
	wantErr := errors.New("fallback media read failed")
	fallback := &talentsFallback{
		Storage: cas.NewMemory(),
		onFetch: func(ctx context.Context, desc ocispec.Descriptor) error {
			if desc.Digest == blob.Digest {
				return wantErr
			}
			return nil
		},
	}
	store, err := NewWithFallbackStorage(t.TempDir(), fallback)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Push(ctx, manifest, bytes.NewReader(manifestJSON)); !errors.Is(err, wantErr) {
		t.Fatalf("manifest Push error = %v, want propagated %v", err, wantErr)
	}
}

func TestTalentsRestoreAllowsManifestBeforeMissingBlob(t *testing.T) {
	ctx := context.Background()
	body := []byte("not in fallback yet")
	blob := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(body),
		Size:      int64(len(body)),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: "late.bin",
		},
	}
	manifestJSON, manifest := talentsManifest(t, blob)
	store, err := NewWithFallbackStorage(t.TempDir(), cas.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Push(ctx, manifest, bytes.NewReader(manifestJSON)); err != nil {
		t.Fatalf("manifest-before-blob Push error = %v, want nil", err)
	}
}
