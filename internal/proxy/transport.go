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
//
// On error, http.Client.Do may return both a non-nil response (the last hop in
// the chain, e.g. the final 302 in a redirect loop) and a non-nil error. We
// drop the response so goproxy takes its error path; otherwise the leaked 302
// would be passed through to the client and the failure would be silent.
func (r *redirectFollower) RoundTrip(req *http.Request, _ *goproxy.ProxyCtx) (*http.Response, error) {
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
