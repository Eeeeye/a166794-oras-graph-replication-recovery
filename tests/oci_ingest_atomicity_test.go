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

package oci

import (
	"bytes"
	"context"
	_ "crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

type talentsOCIIngestFailReader struct {
	err  error
	sent bool
}

func (r *talentsOCIIngestFailReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial blob"), nil
	}
	return 0, r.err
}

func TestTalentsOCIIngestFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	ingestDir := filepath.Join(root, "ingest")
	if err := os.MkdirAll(ingestDir, 0700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(ingestDir, "keep-unrelated")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	body := []byte("complete blob after retry")
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)
	wantErr := errors.New("blob source failed")
	err = store.Push(ctx, desc, &talentsOCIIngestFailReader{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Push() error = %v, want causal %v", err, wantErr)
	}
	matches, err := filepath.Glob(filepath.Join(ingestDir, desc.Digest.Encoded()+"_*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("failed Push() leaked ingest files: %v", matches)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "unrelated" {
		t.Fatalf("failed Push() changed unrelated ingest file: content=%q err=%v", got, err)
	}
	if exists, err := store.Exists(ctx, desc); err != nil || exists {
		t.Fatalf("failed Push() published content: exists=%v err=%v", exists, err)
	}

	mismatchDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, bytes.Repeat([]byte{'x'}, len(body)))
	err = store.Push(ctx, mismatchDesc, bytes.NewReader(body))
	if !errors.Is(err, content.ErrMismatchedDigest) {
		t.Fatalf("digest-mismatched Push() error = %v, want %v", err, content.ErrMismatchedDigest)
	}
	matches, err = filepath.Glob(filepath.Join(ingestDir, mismatchDesc.Digest.Encoded()+"_*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("digest-mismatched Push() leaked ingest files: %v", matches)
	}
	if exists, err := store.Exists(ctx, mismatchDesc); err != nil || exists {
		t.Fatalf("digest-mismatched Push() published content: exists=%v err=%v", exists, err)
	}

	if err := store.Push(ctx, desc, bytes.NewReader(body)); err != nil {
		t.Fatalf("retry Push() error = %v", err)
	}
	rc, err := store.Fetch(ctx, desc)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("retry Fetch() errors: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("retry content = %q, want %q", got, body)
	}
	blobPath := filepath.Join(root, "blobs", desc.Digest.Algorithm().String(), desc.Digest.Encoded())
	info, err := os.Stat(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0444 {
		t.Fatalf("retry blob permissions = %#o, want 0444", got)
	}
	entries, err := os.ReadDir(ingestDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(unrelated) {
		t.Fatalf("successful Push() left unexpected ingest entries: %v", entries)
	}
}
