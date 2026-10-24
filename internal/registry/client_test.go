package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPClientSendsUserAgent(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newHTTPClient(0, nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got != userAgent {
		t.Errorf("User-Agent = %q, want %q", got, userAgent)
	}
}

// TestPolicyTransportRefusesDowngradeAndLongChains drives real redirects through the transport, the layer go-containerregistry's own clients use too.
func TestPolicyTransportRefusesDowngradeAndLongChains(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer plain.Close()
	var tlsSrv *httptest.Server
	tlsSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/downgrade":
			http.Redirect(w, r, plain.URL+"/", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, tlsSrv.URL+"/loop", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, tlsSrv.URL+"/done", http.StatusFound)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer tlsSrv.Close()

	client := &http.Client{Transport: policyTransport{next: tlsSrv.Client().Transport}}
	get := func(u string) error {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := get(tlsSrv.URL + "/downgrade"); err == nil || !strings.Contains(err.Error(), "https to http") {
		t.Errorf("https -> http redirect: err = %v, want a refusal", err)
	}
	if err := get(tlsSrv.URL + "/loop"); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Errorf("redirect loop: err = %v, want it stopped after %d hops", err, maxRedirects)
	}
	if err := get(tlsSrv.URL + "/hop"); err != nil {
		t.Errorf("one https -> https hop: %v, want it followed", err)
	}
}

type countingTransport struct {
	next http.RoundTripper
	n    *atomic.Int32
}

func (t countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.n.Add(1)
	return t.next.RoundTrip(r)
}

// TestRemoteCallsUseThePolicyTransport: go-containerregistry builds its own clients, so hand it the transport.
func TestRemoteCallsUseThePolicyTransport(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v2/charts/app/tags/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"charts/app","tags":["1.0.0","1.1.0"]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	host := "localhost:" + strings.Split(ts.Listener.Addr().String(), ":")[1]

	var n atomic.Int32
	cache := NewIndexCache(withAnonymousAuth())
	cache.transport = countingTransport{next: cache.transport, n: &n}
	if _, err := Latest(context.Background(), cache, "oci", host+"/charts", "app", "1.0.0", ScopeAll); err != nil {
		t.Fatal(err)
	}
	if n.Load() == 0 {
		t.Error("remote.List bypassed the cache's transport")
	}
}
