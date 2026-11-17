package testutils

import "testing"

func TestValidateLiveServer(t *testing.T) {
	for _, server := range []string{"https://api.nistro.ai", "https://api.nistro.ai/", "https://api.nistro.ai:443"} {
		if err := ValidateLiveServer(server, "api.nistro.ai"); err != nil {
			t.Fatalf("approved server rejected: %v", err)
		}
	}
	for _, server := range []string{
		"http://api.nistro.ai", "https://unapproved.example", "https://api.nistro.ai.evil.example",
		"https://user:secret@api.nistro.ai", "https://api.nistro.ai:8443", "https://api.nistro.ai/path",
		"https://api.nistro.ai?", "https://api.nistro.ai?token=secret", "https://api.nistro.ai#fragment",
	} {
		if ValidateLiveServer(server, "api.nistro.ai") == nil {
			t.Error("unsafe API origin accepted")
		}
	}
	for _, allowlist := range []string{"", "*.nistro.ai", "https://api.nistro.ai", "api.nistro.ai,"} {
		if ValidateLiveServer("https://api.nistro.ai", allowlist) == nil {
			t.Error("invalid allowlist accepted")
		}
	}
}
