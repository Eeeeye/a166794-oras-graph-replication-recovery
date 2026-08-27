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
