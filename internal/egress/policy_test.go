package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestPolicyRejectsPrivateAddressAndAllowsExplicitHost(t *testing.T) {
	policy := Policy{DenyPrivateNetworks: true, AllowedHosts: []string{"*.example.com"}}
	if err := policy.ValidateURL("https://api.example.com/v1"); err != nil {
		t.Fatal(err)
	}
	if err := policy.ValidateURL("http://api.example.com.evil.test"); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected host denial, got %v", err)
	}
	if _, err := policy.ResolveAllowed(context.Background(), "127.0.0.1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected private address denial, got %v", err)
	}
}

func TestClientRevalidatesRedirectAndStripsCrossHostCredentials(t *testing.T) {
	policy := Policy{DenyPrivateNetworks: true, AllowedHosts: []string{"api.example.com", "backup.example.com"}}
	client := policy.Client(nil)
	previous, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	next, _ := http.NewRequest(http.MethodGet, "https://backup.example.com/v1", nil)
	next.Header.Set("Authorization", "Bearer secret")
	next.Header.Set("Cookie", "session=secret")
	if err := client.CheckRedirect(next, []*http.Request{previous}); err != nil {
		t.Fatal(err)
	}
	if next.Header.Get("Authorization") != "" || next.Header.Get("Cookie") != "" {
		t.Fatalf("跨主机重定向保留了凭证: %v", next.Header)
	}
	private, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/latest/meta-data", nil)
	if err := client.CheckRedirect(private, []*http.Request{previous}); !errors.Is(err, ErrDenied) {
		t.Fatalf("重定向未重新执行出站策略: %v", err)
	}
	downgrade, _ := http.NewRequest(http.MethodGet, "http://api.example.com/v1", nil)
	if err := client.CheckRedirect(downgrade, []*http.Request{previous}); !errors.Is(err, ErrDenied) {
		t.Fatalf("TLS 降级未被阻断: %v", err)
	}
}

func TestPolicyRejectsDNSRebindingToPrivateAddress(t *testing.T) {
	policy := Policy{DenyPrivateNetworks: true, AllowedHosts: []string{"api.example.com"}, Resolver: &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("not used") }}}
	if _, err := policy.ResolveAllowed(context.Background(), "127.0.0.1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected literal private address denial, got %v", err)
	}
}

func TestExactAllowlistCanAuthorizePrivateService(t *testing.T) {
	policy := Policy{DenyPrivateNetworks: true, AllowedHosts: []string{"127.0.0.1"}}
	addresses, err := policy.ResolveAllowed(context.Background(), "127.0.0.1")
	if err != nil || len(addresses) != 1 {
		t.Fatalf("exact private service allowlist should pass: %v %v", addresses, err)
	}
}
