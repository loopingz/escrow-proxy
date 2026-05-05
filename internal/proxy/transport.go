package proxy

import (
	"net/http"

	"github.com/elazarl/goproxy"
)

// redirectFollower is a goproxy.RoundTripper that follows upstream redirects
// before returning the final response. It composes an *http.Client so that
// net/http's standard redirect handling is reused: relative-URL resolution,
// cross-host header stripping (Authorization, Cookie, WWW-Authenticate), and
// the 10-hop limit.
//
// This intentionally does more than a stdlib http.RoundTripper, which is
// contractually a single round trip with no redirects. escrow-proxy needs the
// terminal body of a redirect chain so the cache key (computed from the
// original URL) maps to the actual content rather than to a transient redirect
// (e.g. a signed CDN URL with a short expiry). goproxy invokes the upstream
// transport through this single seam, which makes it a clean place to absorb
// the redirect chain.
type redirectFollower struct {
	client *http.Client
}

func newRedirectFollower(base http.RoundTripper) *redirectFollower {
	return &redirectFollower{
		client: &http.Client{
			Transport: base,
			// CheckRedirect: nil → stdlib default: follow up to 10 hops.
		},
	}
}

// RoundTrip implements goproxy.RoundTripper. The ctx parameter is unused; the
// redirect chain is fully internal to http.Client.Do.
func (r *redirectFollower) RoundTrip(req *http.Request, _ *goproxy.ProxyCtx) (*http.Response, error) {
	return r.client.Do(req)
}
