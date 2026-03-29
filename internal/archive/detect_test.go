package archive_test

import (
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		dest string
		want string
	}{
		{"./output.tar.gz", "tgz"},
		{"./output.tgz", "tgz"},
		{"/tmp/archive.tar.gz", "tgz"},
		{"./output/", "cas"},
		{"/tmp/mydir", "cas"},
		{"registry.example.com/repo:tag", "oci"},
		{"ghcr.io/org/repo:v1", "oci"},
		{"localhost:5000/test:latest", "oci"},
	}

	for _, tt := range tests {
		got := archive.DetectFormat(tt.dest)
		if got != tt.want {
			t.Errorf("DetectFormat(%q) = %q, want %q", tt.dest, got, tt.want)
		}
	}
}
