/*
Copyright The ORAS Authors.
Licensed under the Apache License, Version 2.0.
*/

package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
)

type talentsScriptedClient func(*http.Request) (*http.Response, error)

func (f talentsScriptedClient) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func talentsHTTPResponse(req *http.Request, status int, location string) *http.Response {
	header := make(http.Header)
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}
}

func TestTalentsBlobUploadRejectsUntrustedLocationBeforePUT(t *testing.T) {
	body := []byte("credential-bound upload")
	desc := ocispec.Descriptor{Digest: digest.FromBytes(body), Size: int64(len(body))}
	tests := []struct {
		name     string
		location string
	}{
		{"cross host", "https://attacker.example/v2/test/blobs/uploads/evil"},
		{"cross port", "https://registry.example:444/v2/test/blobs/uploads/evil"},
		{"TLS downgrade", "http://registry.example/v2/test/blobs/uploads/evil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var puts atomic.Int32
			client := talentsScriptedClient(func(req *http.Request) (*http.Response, error) {
				req.Header.Set("Authorization", "Bearer origin-secret")
				if req.Method == http.MethodPost {
					return talentsHTTPResponse(req, http.StatusAccepted, tt.location), nil
				}
				puts.Add(1)
				return talentsHTTPResponse(req, http.StatusCreated, ""), nil
			})
			repo, err := remote.NewRepository("registry.example/test")
			if err != nil {
				t.Fatal(err)
			}
			repo.Client = client
			if err := repo.Blobs().Push(context.Background(), desc, bytes.NewReader(body)); err == nil {
				t.Fatal("Blobs.Push() accepted an untrusted upload Location")
			}
			if got := puts.Load(); got != 0 {
				t.Fatalf("follow-up PUTs = %d, want 0", got)
			}
		})
	}
}

func TestTalentsBlobUploadAllowsSameOriginDefaultPort(t *testing.T) {
	body := []byte("same origin")
	desc := ocispec.Descriptor{Digest: digest.FromBytes(body), Size: int64(len(body))}
	for _, location := range []string{
		"https://registry.example:443/v2/test/blobs/uploads/ok",
		"/v2/test/blobs/uploads/relative",
	} {
		t.Run(location, func(t *testing.T) {
			var puts atomic.Int32
			client := talentsScriptedClient(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPost {
					return talentsHTTPResponse(req, http.StatusAccepted, location), nil
				}
				puts.Add(1)
				got, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, body) {
					t.Fatalf("PUT body = %q, want %q", got, body)
				}
				return talentsHTTPResponse(req, http.StatusCreated, ""), nil
			})
			repo, err := remote.NewRepository("registry.example/test")
			if err != nil {
				t.Fatal(err)
			}
			repo.Client = client
			if err := repo.Blobs().Push(context.Background(), desc, bytes.NewReader(body)); err != nil {
				t.Fatalf("same-origin upload failed: %v", err)
			}
			if got := puts.Load(); got != 1 {
				t.Fatalf("follow-up PUTs = %d, want 1", got)
			}
		})
	}
}

func TestTalentsTagListingHasIndependentPageBound(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.URL.Query().Get("n"); got != "7" {
			t.Errorf("tag page size = %q, want 7", got)
		}
		w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", r.URL.RequestURI()))
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "test", "tags": []string{"v1"}})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	registry, err := remote.NewRegistry(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	registry.PlainHTTP = true
	registry.TagListPageSize = 7
	registry.TagListMaxPages = 3
	repository, err := registry.Repository(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int32
	err = repository.Tags(context.Background(), "", func(tags []string) error {
		callbacks.Add(1)
		return nil
	})
	if !errors.Is(err, errdef.ErrTooManyPages) {
		t.Fatalf("Tags() error = %v, want %v", err, errdef.ErrTooManyPages)
	}
	if requests.Load() != 3 || callbacks.Load() != 3 {
		t.Fatalf("requests/callbacks = %d/%d, want 3/3", requests.Load(), callbacks.Load())
	}
}

func TestTalentsReferrerListingHasIndependentPageBound(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.URL.Query().Get("n"); got != "5" {
			t.Errorf("referrer page size = %q, want 5", got)
		}
		w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
		w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", r.URL.RequestURI()))
		_ = json.NewEncoder(w).Encode(ocispec.Index{
			Versioned: specsVersioned(),
			MediaType: ocispec.MediaTypeImageIndex,
			Manifests: []ocispec.Descriptor{{
				MediaType: ocispec.MediaTypeImageManifest,
				Digest:    digest.FromString("referrer"),
				Size:      int64(len("referrer")),
			}},
		})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	registry, err := remote.NewRegistry(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	registry.PlainHTTP = true
	registry.ReferrerListPageSize = 5
	registry.ReferrerListMaxPages = 2
	repositoryValue, err := registry.Repository(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	repository := repositoryValue.(*remote.Repository)
	if err := repository.SetReferrersCapability(true); err != nil {
		t.Fatal(err)
	}
	subjectBody := []byte("subject")
	subject := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromBytes(subjectBody), Size: int64(len(subjectBody))}
	var callbacks atomic.Int32
	err = repository.Referrers(context.Background(), subject, "", func([]ocispec.Descriptor) error {
		callbacks.Add(1)
		return nil
	})
	if !errors.Is(err, errdef.ErrTooManyPages) {
		t.Fatalf("Referrers() error = %v, want %v", err, errdef.ErrTooManyPages)
	}
	if requests.Load() != 2 || callbacks.Load() != 2 {
		t.Fatalf("requests/callbacks = %d/%d, want 2/2", requests.Load(), callbacks.Load())
	}
}

func TestTalentsTagAndReferrerZeroBoundsPreserveFinitePagination(t *testing.T) {
	// A zero maximum remains compatible with an ordinary one-page response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tags/list") {
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "test", "tags": []string{"v1"}})
			return
		}
		w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
		_ = json.NewEncoder(w).Encode(ocispec.Index{Versioned: specsVersioned(), MediaType: ocispec.MediaTypeImageIndex})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	repo, err := remote.NewRepository(u.Host + "/test")
	if err != nil {
		t.Fatal(err)
	}
	repo.PlainHTTP = true
	if err := repo.Tags(context.Background(), "", func([]string) error { return nil }); err != nil {
		t.Fatalf("finite Tags() with zero bound: %v", err)
	}
	if err := repo.SetReferrersCapability(true); err != nil {
		t.Fatal(err)
	}
	body := []byte("subject")
	desc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromBytes(body), Size: int64(len(body))}
	if err := repo.Referrers(context.Background(), desc, "", func([]ocispec.Descriptor) error { return nil }); err != nil {
		t.Fatalf("finite Referrers() with zero bound: %v", err)
	}
}

func specsVersioned() specs.Versioned {
	return specs.Versioned{SchemaVersion: 2}
}
