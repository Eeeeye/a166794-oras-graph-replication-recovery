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
	body := []byte("complete blob after retry")
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, body)
	wantErr := errors.New("blob source failed")
	err = store.Push(ctx, desc, &talentsOCIIngestFailReader{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Push() error = %v, want causal %v", err, wantErr)
	}
	matches, err := filepath.Glob(filepath.Join(root, "ingest", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("failed Push() leaked ingest files: %v", matches)
	}
	if exists, err := store.Exists(ctx, desc); err != nil || exists {
		t.Fatalf("failed Push() published content: exists=%v err=%v", exists, err)
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
	entries, err := os.ReadDir(filepath.Join(root, "ingest"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("successful Push() left ingest entries: %v", entries)
	}
}
