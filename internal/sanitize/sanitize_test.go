package sanitize

import (
	"strings"
	"testing"
)

func TestURLRemovesCredentialsQueryAndFragment(t *testing.T) {
	got := URL("https://user:password@example.test/v1/responses?token=secret#private")
	if got != "https://example.test/v1/responses" {
		t.Fatalf("sanitized URL = %q", got)
	}
	if got := URL("/v1/responses?api_key=secret"); got != "/v1/responses" {
		t.Fatalf("sanitized request URI = %q", got)
	}
}

func TestTextRemovesURLsAndAssignedSecrets(t *testing.T) {
	got := Text(`Post "https://hooks.example.test/path?token=secret": token=plain Bearer hidden`)
	for _, secret := range []string{"token=secret", "token=plain", "Bearer hidden"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized text leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "https://hooks.example.test/path") {
		t.Fatalf("sanitized text lost safe URL identity: %s", got)
	}
}
