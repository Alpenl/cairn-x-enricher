package extension

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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

// blockPrivateIP rejects loopback, private, link-local, unspecified, multicast
// and the cloud metadata address. It checks the actual resolved IP, not the
// hostname string, so a DNS rebinding answer cannot pass a name check.
func blockPrivateIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("%w: unresolved address", ErrFetchBlocked)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return fmt.Errorf("%w: non-public address %s", ErrFetchBlocked, ip)
	}
	// 169.254.169.254 is covered by IsLinkLocalUnicast, but keep an explicit
	// check so the intent is obvious.
	if ip.String() == "169.254.169.254" {
		return fmt.Errorf("%w: metadata address", ErrFetchBlocked)
	}
	return nil
}

// ValidateURL checks scheme, credentials, port and resolved addresses. An IP
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
