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

package ioutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type talentsIngestFailReader struct {
	err  error
	sent bool
}

func (r *talentsIngestFailReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial credential"), nil
	}
	return 0, r.err
}

func TestTalentsCredentialIngestFailureIsAtomic(t *testing.T) {
	dir := t.TempDir()
	unrelated := filepath.Join(dir, "keep-existing-credential-file")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("credential source failed")
	_, err := Ingest(dir, &talentsIngestFailReader{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ingest() error = %v, want causal %v", err, wantErr)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "oras_credstore_temp_*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("failed Ingest() leaked temporary files: %v", matches)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "unrelated" {
		t.Fatalf("failed Ingest() changed unrelated file: content=%q err=%v", got, err)
	}

	content := "credential after retry"
	path, err := Ingest(dir, strings.NewReader(content))
	if err != nil {
		t.Fatalf("retry Ingest() error = %v", err)
	}
	defer os.Remove(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("retry content = %q, want %q", got, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("retry permissions = %#o, want 0600", got)
	}
}
