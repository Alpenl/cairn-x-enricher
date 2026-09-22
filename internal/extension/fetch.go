package extension

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// FetchPolicy constrains the controlled external-link adapter. Fetching is the
// highest-risk extension: an untrusted URL must not be able to reach internal
// services, metadata endpoints or a redirect target that does.
type FetchPolicy struct {
	// AllowedHosts is an explicit allowlist. An empty allowlist denies
	// everything; there is no "allow all" mode.
	AllowedHosts map[string]bool
	// MaxBytes bounds the response body.
	MaxBytes int64
	// Timeout bounds the whole request including redirects.
	Timeout time.Duration
	// AllowedContentTypes is the MIME allowlist.
	AllowedContentTypes []string
	// MaxRedirects bounds each hop.
	MaxRedirects int
}

// DefaultFetchPolicy is restrictive.
func DefaultFetchPolicy(allowed []string) FetchPolicy {
	hosts := map[string]bool{}
	for _, host := range allowed {
		hosts[strings.ToLower(strings.TrimSpace(host))] = true
	}
	return FetchPolicy{
		AllowedHosts: hosts, MaxBytes: 2 << 20, Timeout: 15 * time.Second,
		AllowedContentTypes: []string{"text/html", "text/plain", "application/xhtml+xml"},
		MaxRedirects:        3,
	}
}

// ErrFetchBlocked reports a URL the policy refuses to fetch.
var ErrFetchBlocked = errors.New("external fetch blocked by policy")

// These special-purpose ranges are not ordinary public HTTP destinations.
// The policy deliberately also excludes protocol anycasts and transition
// prefixes, even where a more-specific IANA assignment is globally reachable.
// Sources (checked 2026-09-22):
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
var blockedFetchPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// blockPrivateIP checks actual resolved bytes, including IPv4-mapped IPv6.
// IsGlobalUnicast alone does not exclude shared/documentation/reserved space.
func blockPrivateIP(ip net.IP) error {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("%w: unresolved address", ErrFetchBlocked)
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return fmt.Errorf("%w: non-public address %s", ErrFetchBlocked, addr)
	}
	for _, prefix := range blockedFetchPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: special-purpose address %s", ErrFetchBlocked, addr)
		}
	}
	return nil
}

// ValidateURL checks scheme, credentials, port and allowlisted names.
// DialGuard checks resolved addresses when opening each connection. An IP
// literal is rejected outright because it cannot be allowlisted by name.
func ValidateURL(policy FetchPolicy, raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: parse", ErrFetchBlocked)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q", ErrFetchBlocked, parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%w: credentials in URL", ErrFetchBlocked)
	}
	if parsed.Port() != "" && parsed.Port() != "80" && parsed.Port() != "443" {
		return nil, fmt.Errorf("%w: non-standard port", ErrFetchBlocked)
	}
	host := strings.ToLower(parsed.Hostname())
	if net.ParseIP(host) != nil {
		return nil, fmt.Errorf("%w: IP literal", ErrFetchBlocked)
	}
	if !policy.AllowedHosts[host] {
		return nil, fmt.Errorf("%w: host %q not allowlisted", ErrFetchBlocked, host)
	}
	return parsed, nil
}

// DialGuard resolves once, validates the complete answer, then dials only
// numeric addresses from that answer. The HTTP transport retains the original
// URL host for Host and TLS SNI; the dialer must never resolve the name again.
func DialGuard(timeout time.Duration) func(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	return dialGuard(timeout, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

func dialGuard(timeout time.Duration,
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var ips []net.IPAddr
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IPAddr{{IP: ip}}
		} else {
			ips, err = lookup(ctx, host)
			if err != nil {
				return nil, err
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("%w: no addresses", ErrFetchBlocked)
		}
		// Validate every answer before opening any connection. A mixed public /
		// private answer is refused even if the public address would work.
		for _, addr := range ips {
			if err := blockPrivateIP(addr.IP); err != nil {
				return nil, err
			}
			if addr.Zone != "" {
				return nil, fmt.Errorf("%w: scoped address", ErrFetchBlocked)
			}
		}
		var targets []string
		for _, addr := range ips {
			if (network == "tcp4" && addr.IP.To4() == nil) || (network == "tcp6" && addr.IP.To4() != nil) {
				continue
			}
			targets = append(targets, net.JoinHostPort(addr.IP.String(), port))
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("%w: no addresses for network %s", ErrFetchBlocked, network)
		}
		var failures []error
		for index, target := range targets {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Share the remaining total budget so an unreachable first address
			// does not prevent a later validated address from being attempted.
			attemptCtx := ctx
			cancel := func() {}
			if deadline, ok := ctx.Deadline(); ok && index+1 < len(targets) {
				attemptCtx, cancel = context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(targets)-index))
			}
			connection, err := dial(attemptCtx, network, target)
			cancel()
			if err == nil {
				return connection, nil
			}
			failures = append(failures, err)
		}
		return nil, errors.Join(failures...)
	}
}

// ExternalContent is the fetched material. It never overwrites the original.
type ExternalContent struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated"`
}

// ControlledFetcher fetches allowlisted external material with every hop
// checked. It carries no credentials and never forwards the internal token.
func ControlledFetcher(policy FetchPolicy, client *http.Client) (*http.Client, error) {
	if len(policy.AllowedHosts) == 0 {
		return nil, fmt.Errorf("%w: empty allowlist denies all fetches", ErrFetchBlocked)
	}
	if client == nil {
		transport := &http.Transport{
			DialContext:           DialGuard(policy.Timeout),
			DisableKeepAlives:     true,
			MaxIdleConns:          1,
			ResponseHeaderTimeout: policy.Timeout,
		}
		client = &http.Client{Transport: transport, Timeout: policy.Timeout}
	}
	// Clone before installing redirect policy: concurrent executions must not
	// mutate a shared http.Client.
	copyClient := *client
	client = &copyClient
	// Each redirect hop is re-validated against the policy, so a redirect to a
	// private or non-allowlisted host is refused.
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= policy.MaxRedirects {
			return fmt.Errorf("%w: too many redirects", ErrFetchBlocked)
		}
		_, err := ValidateURL(policy, request.URL.String())
		return err
	}
	return client, nil
}

// Fetch retrieves one allowlisted URL. It validates the URL, checks the
// content type, bounds the body and never sends the internal token.
func Fetch(ctx context.Context, client *http.Client, policy FetchPolicy, raw string) (ExternalContent, error) {
	if _, err := ValidateURL(policy, raw); err != nil {
		return ExternalContent{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return ExternalContent{}, fmt.Errorf("%w: request", ErrFetchBlocked)
	}
	// No Authorization header is ever set: credentials never cross origins.
	request.Header.Set("User-Agent", "cairn-enricher/1.0")
	request.Header.Set("Accept", strings.Join(policy.AllowedContentTypes, ", "))
	response, err := client.Do(request)
	if err != nil {
		return ExternalContent{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ExternalContent{}, fmt.Errorf("external fetch returned HTTP %d", response.StatusCode)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	allowed := false
	for _, candidate := range policy.AllowedContentTypes {
		if contentType == candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		return ExternalContent{}, fmt.Errorf("%w: content type %q", ErrFetchBlocked, contentType)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, policy.MaxBytes+1))
	if err != nil {
		return ExternalContent{}, err
	}
	truncated := int64(len(body)) > policy.MaxBytes
	if truncated {
		body = body[:policy.MaxBytes]
	}
	return ExternalContent{
		URL: raw, ContentType: contentType,
		Text: stripMarkup(string(body)), Truncated: truncated,
	}, nil
}

// stripMarkup removes tags so stored external text cannot smuggle markup into
// a later render, while keeping the readable text.
func stripMarkup(value string) string {
	var builder strings.Builder
	inTag := false
	for _, r := range value {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			builder.WriteRune(' ')
		case !inTag:
			builder.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}
