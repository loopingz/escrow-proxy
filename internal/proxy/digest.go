package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// ociDigestPath matches OCI Distribution v2 URLs that pin a blob or
// manifest by its sha256 content digest. The capture is the lowercase
// 64-hex digest.
//
// Examples that match:
//
//	/v2/library/elasticsearch/blobs/sha256:450c...
//	/v2/library/elasticsearch/manifests/sha256:88ff...
//	/v2/some/nested/repo/path/blobs/sha256:abcd...
//
// Examples that do NOT match: tag refs (e.g. /manifests/1.0), digests in
// non-/v2 paths, uploads endpoints, or non-sha256 algorithms.
var ociDigestPath = regexp.MustCompile(`^/v2/[^/]+(?:/[^/]+)+/(?:blobs|manifests)/sha256:([a-fA-F0-9]{64})$`)

// ExtractOCIDigest returns the lowercase sha256 hex digest claimed by an
// OCI v2 URL path, or "" if the path does not pin content by digest.
//
// Verification at the proxy is only sound when the URL itself encodes the
// expected digest — that is what makes blob/manifest-by-digest URLs
// immutable under the OCI Distribution spec. Tag refs, upload sessions,
// and non-OCI paths return "".
func ExtractOCIDigest(urlPath string) string {
	m := ociDigestPath.FindStringSubmatch(urlPath)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// VerifyDigest reports whether sha256(body) matches the hex-encoded
// digest (case-insensitive). An empty digest is treated as a no-op and
// returns true, so callers can chain ExtractOCIDigest -> VerifyDigest
// without a separate nil check.
func VerifyDigest(body []byte, digest string) bool {
	if digest == "" {
		return true
	}
	sum := sha256.Sum256(body)
	return strings.EqualFold(hex.EncodeToString(sum[:]), digest)
}
