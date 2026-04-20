package main

import "testing"

func TestBuildVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		commit  string
		date    string
		want    string
	}{
		{
			name:    "populated",
			version: "1.2.3",
			commit:  "abcdef1",
			date:    "2026-04-19T10:00:00Z",
			want:    "escrow-proxy 1.2.3 (commit abcdef1, built 2026-04-19T10:00:00Z)",
		},
		{
			name:    "defaults",
			version: "dev",
			commit:  "none",
			date:    "unknown",
			want:    "escrow-proxy dev (commit none, built unknown)",
		},
		{
			name:    "empty inputs",
			version: "",
			commit:  "",
			date:    "",
			want:    "escrow-proxy  (commit , built )",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildVersion(tc.version, tc.commit, tc.date)
			if got != tc.want {
				t.Fatalf("buildVersion(%q, %q, %q) = %q, want %q",
					tc.version, tc.commit, tc.date, got, tc.want)
			}
		})
	}
}
