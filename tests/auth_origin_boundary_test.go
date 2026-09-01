// Copyright The ORAS Authors.
// Licensed under the Apache License, Version 2.0.

package auth_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	auth "oras.land/oras-go/v2/registry/remote/auth"
)

type talentsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f talentsRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func talentsResponse(req *http.Request, status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func TestTalentsAuthContainsCredentialsAcrossRedirectBoundaries(t *testing.T) {
	const authorization = "Basic origin-secret"
	tests := []struct {
		name       string
		origin     string
		target     string
		wantHeader bool
	}{
		{
			name:       "same origin path",
			origin:     "https://registry.example.test/v2/start",
			target:     "https://registry.example.test/v2/target",
			wantHeader: true,
		},
		{
			name:       "default HTTPS port is the same origin",
			origin:     "https://registry.example.test/v2/start",
			target:     "https://registry.example.test:443/v2/target",
			wantHeader: true,
		},
		{
			name:       "different port is a different origin",
			origin:     "https://registry.example.test:5000/v2/start",
			target:     "https://registry.example.test:6000/v2/target",
			wantHeader: false,
		},
		{
			name:       "different scheme is a different origin",
			origin:     "https://registry.example.test/v2/start",
			target:     "http://registry.example.test/v2/target",
			wantHeader: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			var redirectCallbacks atomic.Int32
			var targetAuthorization string
			transport := talentsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch calls.Add(1) {
				case 1:
					header := make(http.Header)
					header.Set("Location", tt.target)
					return talentsResponse(req, http.StatusTemporaryRedirect, header, ""), nil
				case 2:
					targetAuthorization = req.Header.Get("Authorization")
					return talentsResponse(req, http.StatusOK, nil, "ok"), nil
				default:
					return nil, fmt.Errorf("unexpected redirect request %d", calls.Load())
				}
			})

			client := &auth.Client{Client: &http.Client{
				Transport: transport,
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
					redirectCallbacks.Add(1)
					return nil
				},
			}}
			req, err := http.NewRequest(http.MethodGet, tt.origin, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", authorization)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Client.Do() error = %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("Client.Do() status = %d, want 200", resp.StatusCode)
			}
			if redirectCallbacks.Load() != 1 {
				t.Fatalf("caller CheckRedirect calls = %d, want 1", redirectCallbacks.Load())
			}
			if got := targetAuthorization != ""; got != tt.wantHeader {
				t.Fatalf("redirected Authorization present = %v, want %v", got, tt.wantHeader)
			}
			if tt.wantHeader && targetAuthorization != authorization {
				t.Fatalf("same-origin Authorization = %q, want original value", targetAuthorization)
			}
		})
	}
}

func TestTalentsAuthRejectsCrossOriginChallengeBeforeCredentialLookup(t *testing.T) {
	var sinkAuthorization atomic.Value
	sinkAuthorization.Store("")
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sinkAuthorization.Store(r.Header.Get("Authorization"))
		w.Header().Set("Www-Authenticate", `Basic realm="redirect-target"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer sink.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	var credentialCalls atomic.Int32
	client := &auth.Client{Credential: func(context.Context, string) (auth.Credential, error) {
		credentialCalls.Add(1)
		return auth.Credential{Username: "origin-user", Password: "origin-password"}, nil
	}}
	req, err := http.NewRequest(http.MethodGet, origin.URL+"/v2/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Client.Do() status = %d, want redirected 401", resp.StatusCode)
	}
	if credentialCalls.Load() != 0 {
		t.Fatalf("credential lookups = %d, want 0 for a cross-origin challenge", credentialCalls.Load())
	}
	if got := sinkAuthorization.Load().(string); got != "" {
		t.Fatalf("redirect target received Authorization %q", got)
	}
}

func TestTalentsAuthPreservesSameOriginRedirectBehavior(t *testing.T) {
	const authorization = "Bearer same-origin-token"
	var gotAuthorization atomic.Value
	gotAuthorization.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
		case "/target":
			gotAuthorization.Store(r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var redirectCalls atomic.Int32
	client := &auth.Client{Client: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		redirectCalls.Add(1)
		return nil
	}}}
	req, err := http.NewRequest(http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", authorization)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	resp.Body.Close()
	if got := gotAuthorization.Load().(string); got != authorization {
		t.Fatalf("same-origin Authorization = %q, want %q", got, authorization)
	}
	if redirectCalls.Load() != 1 {
		t.Fatalf("caller CheckRedirect calls = %d, want 1", redirectCalls.Load())
	}
}

func TestTalentsAuthRejectsUnsafeBearerRealmsBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name        string
		registryURL string
		realm       string
	}{
		{"empty realm", "https://registry.example.test/v2/", ""},
		{"malformed realm", "https://registry.example.test/v2/", "://bad-url"},
		{"HTTPS realm without host", "https://registry.example.test/v2/", "https:///token"},
		{"unsupported scheme", "https://registry.example.test/v2/", "file:///etc/passwd"},
		{"TLS downgrade", "https://registry.example.test/v2/", "http://auth.example.test/token"},
		{"link-local metadata address", "http://registry.example.test/v2/", "http://169.254.169.254/latest/meta-data/"},
		{"cross-host loopback", "http://registry.example.test/v2/", "http://127.0.0.1:9090/token"},
		{"cross-host private address", "http://registry.example.test/v2/", "http://10.0.0.5/token"},
		{"cross-host unspecified address", "http://registry.example.test/v2/", "http://0.0.0.0/token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			var credentialCalls atomic.Int32
			transport := talentsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if requests.Add(1) != 1 {
					return nil, fmt.Errorf("unsafe realm was contacted: %s", req.URL)
				}
				header := make(http.Header)
				header.Set("Www-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="registry"`, tt.realm))
				return talentsResponse(req, http.StatusUnauthorized, header, ""), nil
			})
			client := &auth.Client{
				Client: &http.Client{Transport: transport},
				Credential: func(context.Context, string) (auth.Credential, error) {
					credentialCalls.Add(1)
					return auth.Credential{Username: "user", Password: "password"}, nil
				},
			}
			req, err := http.NewRequest(http.MethodGet, tt.registryURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil {
				t.Fatal("Client.Do() error = nil, want unsafe realm rejection")
			}
			if requests.Load() != 1 {
				t.Fatalf("HTTP requests = %d, want only the registry challenge", requests.Load())
			}
			if credentialCalls.Load() != 0 {
				t.Fatalf("credential lookups = %d, want 0", credentialCalls.Load())
			}
		})
	}
}

func TestTalentsAuthAllowsCompatibleBearerRealms(t *testing.T) {
	t.Run("public cross-host HTTPS token service", func(t *testing.T) {
		const (
			username = "public-user"
			password = "public-password"
			token    = "public-token"
		)
		wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
		var tokenRequests atomic.Int32
		transport := talentsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Host {
			case "registry.example.test":
				if req.Header.Get("Authorization") == "Bearer "+token {
					return talentsResponse(req, http.StatusOK, nil, "ok"), nil
				}
				header := make(http.Header)
				header.Set("Www-Authenticate", `Bearer realm="https://auth.example.test/token",service="registry"`)
				return talentsResponse(req, http.StatusUnauthorized, header, ""), nil
			case "auth.example.test":
				tokenRequests.Add(1)
				if req.Header.Get("Authorization") != wantBasic {
					return nil, fmt.Errorf("token service Authorization = %q", req.Header.Get("Authorization"))
				}
				return talentsResponse(req, http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"token":"`+token+`"}`), nil
			default:
				return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
			}
		})
		client := &auth.Client{
			Client: &http.Client{Transport: transport},
			Credential: func(context.Context, string) (auth.Credential, error) {
				return auth.Credential{Username: username, Password: password}, nil
			},
		}
		req, _ := http.NewRequest(http.MethodGet, "https://registry.example.test/v2/", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Client.Do() error = %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || tokenRequests.Load() != 1 {
			t.Fatalf("status = %d, token requests = %d; want 200 and one token request", resp.StatusCode, tokenRequests.Load())
		}
	})

	t.Run("same-host loopback token service", func(t *testing.T) {
		const token = "loopback-token"
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":%q}`, token)
		}))
		defer tokenServer.Close()

		registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer "+token {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="loopback"`, tokenServer.URL))
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer registry.Close()

		registryURL, err := url.Parse(registry.URL)
		if err != nil {
			t.Fatal(err)
		}
		client := &auth.Client{Credential: func(_ context.Context, host string) (auth.Credential, error) {
			if host != registryURL.Host {
				return auth.EmptyCredential, fmt.Errorf("credential host = %q, want %q", host, registryURL.Host)
			}
			return auth.EmptyCredential, nil
		}}
		req, _ := http.NewRequest(http.MethodGet, registry.URL+"/v2/", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Client.Do() error = %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Client.Do() status = %d, want 200", resp.StatusCode)
		}
	})
}
