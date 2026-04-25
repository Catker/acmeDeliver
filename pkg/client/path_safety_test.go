package client

import (
	"path/filepath"
	"testing"
)

func TestSafeDomainDir(t *testing.T) {
	baseDir := t.TempDir()

	tests := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{name: "valid", domain: "example.com", wantErr: false},
		{name: "empty", domain: "", wantErr: true},
		{name: "parent traversal", domain: "../etc", wantErr: true},
		{name: "nested path", domain: "a/b", wantErr: true},
		{name: "backslash", domain: `a\b`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeDomainDir(baseDir, tt.domain)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			want := filepath.Join(baseDir, tt.domain)
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestSafeDomainFilePath(t *testing.T) {
	baseDir := t.TempDir()

	tests := []struct {
		name     string
		domain   string
		filename string
		wantErr  bool
	}{
		{name: "valid", domain: "example.com", filename: "cert.pem", wantErr: false},
		{name: "parent traversal", domain: "example.com", filename: "../passwd", wantErr: true},
		{name: "nested path", domain: "example.com", filename: "subdir/key.pem", wantErr: true},
		{name: "absolute path", domain: "example.com", filename: "/tmp/x", wantErr: true},
		{name: "bad domain", domain: "../etc", filename: "cert.pem", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeDomainFilePath(baseDir, tt.domain, tt.filename)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			want := filepath.Join(baseDir, tt.domain, tt.filename)
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
