package registry

import (
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
)

// Test-only cache options: production code configures a cache through NewIndexCache's exported options only.

// withLimits adjusts the fetch limits of one cache (tests).
func withLimits(adjust func(*limits)) CacheOption {
	return func(c *IndexCache) { adjust(&c.limits) }
}

// withHTTPClient replaces the client used for Helm index fetches (tests: a TLS httptest server's client).
func withHTTPClient(client *http.Client) CacheOption {
	return func(c *IndexCache) { c.httpClient = client }
}

// withAnonymousAuth stops the cache from consulting docker credential helpers, which can hang in test environments.
func withAnonymousAuth() CacheOption {
	return func(c *IndexCache) { c.keychain = authn.NewMultiKeychain() }
}

// withVendorAPI points one registry host at a vendor tag API (tests: an httptest server speaking the Quay or Harbor dialect).
func withVendorAPI(host string, api vendorAPI) CacheOption {
	return func(c *IndexCache) { c.vendors[host] = api }
}
