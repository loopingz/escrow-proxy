package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestExtractOCIDigest(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "blob by digest",
			path: "/v2/library/elasticsearch/blobs/sha256:450c1c7eb770e25c3696960ecdf1bfbb9e8a93b9206e8ab7d5eab25e37ee0671",
			want: "450c1c7eb770e25c3696960ecdf1bfbb9e8a93b9206e8ab7d5eab25e37ee0671",
		},
		{
			name: "manifest by digest",
			path: "/v2/library/elasticsearch/manifests/sha256:88fffeb5d08fe55bea1dacb11f8abc1205241b5b32ad4a22c14f85e578d9a90e",
			want: "88fffeb5d08fe55bea1dacb11f8abc1205241b5b32ad4a22c14f85e578d9a90e",
		},
		{
			name: "nested repo name",
			path: "/v2/primal-oxide-268801/arize-internal/mirror/mirror.gcr.io/library/telegraf/blobs/sha256:15c787ed509753ff1c98af9c858bc4e79a8838e2f97f9da94808690f237954a8",
			want: "15c787ed509753ff1c98af9c858bc4e79a8838e2f97f9da94808690f237954a8",
		},
		{
			name: "uppercase hex normalized to lowercase",
			path: "/v2/library/foo/blobs/sha256:ABCDEF0123456789abcdef0123456789ABCDEF0123456789abcdef0123456789",
			want: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name: "tag, not digest",
			path: "/v2/library/elasticsearch/manifests/8.11.3",
			want: "",
		},
		{
			name: "non-v2 path",
			path: "/some/other/blobs/sha256:450c1c7eb770e25c3696960ecdf1bfbb9e8a93b9206e8ab7d5eab25e37ee0671",
			want: "",
		},
		{
			name: "missing repo segment",
			path: "/v2/blobs/sha256:450c1c7eb770e25c3696960ecdf1bfbb9e8a93b9206e8ab7d5eab25e37ee0671",
			want: "",
		},
		{
			name: "wrong digest length (63 hex)",
			path: "/v2/library/foo/blobs/sha256:450c1c7eb770e25c3696960ecdf1bfbb9e8a93b9206e8ab7d5eab25e37ee067",
			want: "",
		},
		{
			name: "non-hex char in digest",
			path: "/v2/library/foo/blobs/sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			want: "",
		},
		{
			name: "other algorithm (sha512)",
			path: "/v2/library/foo/blobs/sha512:450c1c7e",
			want: "",
		},
		{
			name: "blobs/uploads (upload session, not by-digest)",
			path: "/v2/library/foo/blobs/uploads/abc-123",
			want: "",
		},
		{
			name: "empty path",
			path: "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractOCIDigest(tc.path)
			if got != tc.want {
				t.Fatalf("ExtractOCIDigest(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestVerifyDigest(t *testing.T) {
	body := []byte("hello world")
	sum := sha256.Sum256(body)
	correct := hex.EncodeToString(sum[:])

	if !VerifyDigest(body, correct) {
		t.Fatalf("VerifyDigest with correct sha256 returned false")
	}
	if VerifyDigest(body, "0000000000000000000000000000000000000000000000000000000000000000") {
		t.Fatalf("VerifyDigest with wrong sha256 returned true")
	}
	// Case-insensitive comparison: callers may pass uppercase digests.
	if !VerifyDigest(body, "B94D27B9934D3E08A52E52D7DA7DABFAC484EFE37A5380EE9088F7ACE2EFCDE9") {
		t.Fatalf("VerifyDigest with uppercase digest returned false")
	}
	// Empty digest is treated as "nothing to check" — return true so callers
	// can chain ExtractOCIDigest -> VerifyDigest without a nil check.
	if !VerifyDigest(body, "") {
		t.Fatalf("VerifyDigest with empty digest returned false; expected true (no-op)")
	}
}
