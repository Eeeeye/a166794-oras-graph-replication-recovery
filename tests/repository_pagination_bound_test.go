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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
)

func TestTalentsRepositoryCatalogHasHardPageBound(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v2/_catalog" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if request == 1 && r.URL.Query().Get("n") != "17" {
			t.Errorf("first catalog page size query = %q, want 17", r.URL.Query().Get("n"))
		}
		w.Header().Set("Link", `</v2/_catalog>; rel="next"`)
		_ = json.NewEncoder(w).Encode(map[string][]string{"repositories": {"team/project"}})
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := remote.NewRegistry(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	registry.PlainHTTP = true
	registry.RepositoryListPageSize = 17
	registry.RepositoryListMaxPages = 3
	var callbacks atomic.Int32
	err = registry.Repositories(context.Background(), "", func(repos []string) error {
		callbacks.Add(1)
		if len(repos) != 1 || repos[0] != "team/project" {
			t.Fatalf("callback repositories = %v", repos)
		}
		return nil
	})
	if !errors.Is(err, errdef.ErrTooManyPages) {
		t.Fatalf("Repositories() error = %v, want %v", err, errdef.ErrTooManyPages)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("HTTP requests = %d, want hard bound 3", got)
	}
	if got := callbacks.Load(); got != 3 {
		t.Fatalf("callbacks = %d, want exactly fetched pages 3", got)
	}
}

func TestTalentsRepositoryCatalogZeroLimitPreservesFinitePagination(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := requests.Add(1)
		if page == 1 {
			w.Header().Set("Link", `</v2/_catalog?last=first>; rel="next"`)
		}
		_ = json.NewEncoder(w).Encode(map[string][]string{"repositories": {"repo"}})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	registry, err := remote.NewRegistry(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	registry.PlainHTTP = true
	if err := registry.Repositories(context.Background(), "", func([]string) error { return nil }); err != nil {
		t.Fatalf("Repositories() finite zero-limit error = %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("finite pagination requests = %d, want 2", got)
	}
}
