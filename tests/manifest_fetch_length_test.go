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
	"bytes"
	"context"
	_ "crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
)

func TestTalentsManifestFetchIgnoresTransportLengthDivergence(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}`)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("request method = %s, want GET", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", desc.MediaType)
		w.Header().Set("Docker-Content-Digest", desc.Digest.String())
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := remote.NewRepository(u.Host + "/team/project")
	if err != nil {
		t.Fatal(err)
	}
	repo.PlainHTTP = true

	fromHead := desc
	fromHead.Size += 137 // model a HEAD/GET length divergence
	rc, err := repo.Manifests().Fetch(context.Background(), fromHead)
	if err != nil {
		t.Fatalf("Fetch() rejected a digest-valid manifest length divergence: %v", err)
	}
	got, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("manifest stream errors: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("manifest body changed: got %q want %q", got, body)
	}
}

func talentsRemoteRepositoryForHandler(t *testing.T, handler http.Handler) (*remote.Repository, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	u, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	repo, err := remote.NewRepository(u.Host + "/team/project")
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	repo.PlainHTTP = true
	return repo, server.Close
}

func TestTalentsManifestFetchPreservesIntegrityBoundaries(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}`)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, body)

	t.Run("missing Content-Length is accepted", func(t *testing.T) {
		repo, closeServer := talentsRemoteRepositoryForHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", desc.MediaType)
			w.Header().Set("Docker-Content-Digest", desc.Digest.String())
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush() // force a chunked response with unknown length
			_, _ = w.Write(body)
		}))
		defer closeServer()
		fromHead := desc
		fromHead.Size += 51
		rc, err := repo.Manifests().Fetch(context.Background(), fromHead)
		if err != nil {
			t.Fatal("manifest Fetch with unknown Content-Length error =", err)
		}
		got, readErr := io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(got, body) {
			t.Fatalf("chunked manifest body: len=%d read=%v close=%v", len(got), readErr, closeErr)
		}
	})

	t.Run("mismatched media type is rejected", func(t *testing.T) {
		repo, closeServer := talentsRemoteRepositoryForHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.example.wrong")
			w.Header().Set("Docker-Content-Digest", desc.Digest.String())
			_, _ = w.Write(body)
		}))
		defer closeServer()
		if _, err := repo.Manifests().Fetch(context.Background(), desc); err == nil {
			t.Fatal("manifest Fetch accepted a mismatched response media type")
		}
	})

	t.Run("mismatched digest header is rejected", func(t *testing.T) {
		repo, closeServer := talentsRemoteRepositoryForHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", desc.MediaType)
			w.Header().Set("Docker-Content-Digest", content.NewDescriptorFromBytes(desc.MediaType, []byte("other")).Digest.String())
			_, _ = w.Write(body)
		}))
		defer closeServer()
		if _, err := repo.Manifests().Fetch(context.Background(), desc); err == nil {
			t.Fatal("manifest Fetch accepted a mismatched Docker-Content-Digest")
		}
	})

	t.Run("blob Content-Length mismatch remains rejected", func(t *testing.T) {
		blob := []byte("ordinary blob content")
		blobDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, blob)
		repo, closeServer := talentsRemoteRepositoryForHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Docker-Content-Digest", blobDesc.Digest.String())
			w.Header().Set("Content-Length", strconv.FormatInt(blobDesc.Size+1, 10))
			_, _ = w.Write(blob)
		}))
		defer closeServer()
		if _, err := repo.Blobs().Fetch(context.Background(), blobDesc); err == nil {
			t.Fatal("blob Fetch accepted a mismatched Content-Length")
		}
	})
}
