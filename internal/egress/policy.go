package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrDenied        = errors.New("egress target denied by policy")
	ErrInvalidTarget = errors.New("invalid egress target")
)

// Policy controls outbound HTTP destinations. Host patterns are exact names
// or wildcard suffixes such as *.example.com.
type Policy struct {
	DenyPrivateNetworks bool     `json:"denyPrivateNetworks" yaml:"deny-private-networks"`
	AllowedHosts        []string `json:"allowedHosts" yaml:"allowed-hosts"`
	Resolver            *net.Resolver
	DialTimeout         time.Duration
}

func Default() Policy {
	return Policy{DenyPrivateNetworks: true, AllowedHosts: []string{"cli-proxy-api"}, DialTimeout: 10 * time.Second}
}

func (p Policy) Normalize() Policy {
	if p.DialTimeout <= 0 {
		p.DialTimeout = 10 * time.Second
	}
	p.Resolver = p.ResolverOrDefault()
	seen := make(map[string]struct{}, len(p.AllowedHosts))
	allowed := make([]string, 0, len(p.AllowedHosts))
	for _, raw := range p.AllowedHosts {
		value := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(raw, ".")))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		allowed = append(allowed, value)
	}
	p.AllowedHosts = allowed
	return p
}

func (p Policy) ResolverOrDefault() *net.Resolver {
	if p.Resolver != nil {
		return p.Resolver
	}
	return net.DefaultResolver
}

func (p Policy) ValidateURL(raw string) error {
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "http" && target.Scheme != "https" || target.Host == "" || target.User != nil {
		return fmt.Errorf("%w: %s", ErrInvalidTarget, raw)
	}
	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	if host == "" || !p.allowedHost(host) {
		return fmt.Errorf("%w: host %s", ErrDenied, host)
	}
	if ip := net.ParseIP(host); ip != nil && p.DenyPrivateNetworks && privateIP(ip) && !p.exactHost(host) {
		return fmt.Errorf("%w: private address %s", ErrDenied, ip)
	}
	return nil
}

func (p Policy) exactHost(host string) bool {
	for _, pattern := range p.AllowedHosts {
		if !strings.HasPrefix(pattern, "*.") && strings.EqualFold(strings.TrimSuffix(pattern, "."), host) {
			return true
		}
	}
	return false
}

func (p Policy) allowedHost(host string) bool {
	if len(p.AllowedHosts) == 0 {
		return true
	}
	for _, pattern := range p.AllowedHosts {
		pattern = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(pattern, ".")))
		if pattern == host {
			return true
		}
		if strings.HasPrefix(pattern, "*.") && (host == strings.TrimPrefix(pattern, "*.") || strings.HasSuffix(host, "."+strings.TrimPrefix(pattern, "*."))) {
			return true
		}
	}
	return false
}

func (p Policy) ResolveAllowed(ctx context.Context, host string) ([]net.IP, error) {
	p = p.Normalize()
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || !p.allowedHost(host) {
		return nil, fmt.Errorf("%w: host %s", ErrDenied, host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if p.DenyPrivateNetworks && privateIP(ip) && !p.exactHost(host) {
			return nil, fmt.Errorf("%w: private address %s", ErrDenied, ip)
		}
		return []net.IP{ip}, nil
	}
	addresses, err := p.ResolverOrDefault().LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: host %s has no addresses", ErrDenied, host)
	}
	for _, ip := range addresses {
		if p.DenyPrivateNetworks && privateIP(ip) && !p.exactHost(host) {
			return nil, fmt.Errorf("%w: %s resolved to private address %s", ErrDenied, host, ip)
		}
	}
	return addresses, nil
}

func (p Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTarget, err)
	}
	if !p.allowedHost(strings.ToLower(strings.TrimSuffix(host, "."))) {
		return nil, fmt.Errorf("%w: host %s", ErrDenied, host)
	}
	resolveCtx, cancel := context.WithTimeout(ctx, p.DialTimeout)
	defer cancel()
	addresses, err := p.ResolveAllowed(resolveCtx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: p.DialTimeout, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range addresses {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func (p Policy) Transport(base *http.Transport) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	} else {
		base = base.Clone()
	}
	p = p.Normalize()
	base.DialContext = p.DialContext
	return base
}

func (p Policy) Client(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	} else {
		clone := *base
		base = &clone
	}
	base.Transport = p.Transport(asTransport(base.Transport))
	previousRedirect := base.CheckRedirect
	base.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("%w: too many redirects", ErrDenied)
		}
		if err := p.ValidateURL(request.URL.String()); err != nil {
			return err
		}
		if len(via) > 0 {
			previous := via[len(via)-1]
			if previous.URL.Scheme == "https" && request.URL.Scheme != "https" {
				return fmt.Errorf("%w: TLS redirect downgrade", ErrDenied)
			}
			if !strings.EqualFold(previous.URL.Hostname(), request.URL.Hostname()) {
				for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
					request.Header.Del(name)
				}
			}
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	return base
}

// WithExactTarget adds a configured destination as an exact host exception.
// Exact destinations may intentionally be private; wildcard entries never gain
// that privilege.
func (p Policy) WithExactTarget(raw string) Policy {
	target, err := url.Parse(raw)
	if err == nil && target.Hostname() != "" {
		p.AllowedHosts = append(p.AllowedHosts, target.Hostname())
	}
	return p.Normalize()
}

func asTransport(value http.RoundTripper) *http.Transport {
	if value == nil {
		return http.DefaultTransport.(*http.Transport)
	}
	if transport, ok := value.(*http.Transport); ok {
		return transport
	}
	return http.DefaultTransport.(*http.Transport)
}

func privateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	// RFC 6598 and common cloud metadata ranges.
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 || ip4[0] == 169 && ip4[1] == 254
	}
	return ip.IsLinkLocalUnicast()
}

func ValidatePatterns(patterns []string) error {
	for _, raw := range patterns {
		value := strings.TrimSpace(strings.ToLower(raw))
		if value == "" || strings.ContainsAny(value, "/?#@") || strings.Contains(value, "..") && strings.HasPrefix(value, "*.") && !strings.Contains(value[2:], ".") {
			return fmt.Errorf("invalid egress host pattern %q", raw)
		}
		if strings.HasPrefix(value, "*.") && len(value) <= 2 {
			return fmt.Errorf("invalid egress wildcard %q", raw)
		}
	}
	return nil
}

func Port(raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 65535 {
		return 0, ErrInvalidTarget
	}
	return value, nil
}
