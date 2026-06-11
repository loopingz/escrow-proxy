package proxy

import (
	"net/http"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/metrics"
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
	client  *http.Client
	metrics *metrics.Metrics
}

func newRedirectFollower(base http.RoundTripper, m *metrics.Metrics) *redirectFollower {
	return &redirectFollower{
		client: &http.Client{
			Transport: base,
			// CheckRedirect: nil → stdlib default: follow up to 10 hops.
		},
		metrics: m,
	}
}

// upstreamErrorHeader marks a response synthesized by redirectFollower for
// an upstream failure. HandleResponse strips it and uses it to skip
// double-recording the upstream-error metric (already classified here).
const upstreamErrorHeader = "X-Escrow-Proxy-Upstream-Error"

// RoundTrip implements goproxy.RoundTripper. The ctx parameter is unused; the
// redirect chain is fully internal to http.Client.Do.
//
// On error, http.Client.Do may return both a non-nil response (the last hop in
// the chain, e.g. the final 302 in a redirect loop) and a non-nil error. We
// close the leaked body and drop the response; otherwise the 302 would be
// passed through to the client (silent failure) and the open body would leak
// the underlying connection.
//
// Errors are converted into a synthesized 502 rather than returned: goproxy's
// MITM loop skips filterResponse entirely on a RoundTrip error (it just
// closes the client connection), so HandleResponse would never see the
// failure and could not serve a stale revalidation fallback. Synthesizing a
// response keeps the plain-HTTP and MITM paths uniform: HandleResponse always
// runs, serves the fallback when one is armed, and otherwise passes the 502
// to the client.
func (r *redirectFollower) RoundTrip(req *http.Request, _ *goproxy.ProxyCtx) (*http.Response, error) {
	resp, err := r.client.Do(req)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		r.metrics.RecordUpstreamError(metrics.ClassifyUpstreamError(err))
		errResp := goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusBadGateway,
			"escrow-proxy: upstream request failed: "+err.Error())
		errResp.Header.Set(upstreamErrorHeader, "1")
		return errResp, nil
	}
	return resp, nil
}
