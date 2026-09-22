package extension

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestDialGuardPinsValidatedAnswerAndFallsBackWithoutResolvingAgain(t *testing.T) {
	lookups := 0
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookups++
		if host != "allowed.example" {
			t.Fatalf("unexpected host %q", host)
		}
		if lookups > 1 {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("2001:4860:4860::8888")}}, nil
	}
	var attempts []string
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatalf("network %s", network)
		}
		attempts = append(attempts, address)
		if address == "8.8.8.8:443" {
			return nil, errors.New("first address unavailable")
		}
		if address != "[2001:4860:4860::8888]:443" {
			t.Fatalf("unvalidated target %q", address)
		}
		return client, nil
	}
	conn, err := dialGuard(time.Second, lookup, dial)(context.Background(), "tcp", "allowed.example:443")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if lookups != 1 || !reflect.DeepEqual(attempts, []string{"8.8.8.8:443", "[2001:4860:4860::8888]:443"}) {
		t.Fatalf("lookup=%d attempts=%v", lookups, attempts)
	}
}

func TestDialGuardRefusesWholeMixedAnswerBeforeAnyConnection(t *testing.T) {
	for _, blocked := range []string{"127.0.0.1", "::1", "10.1.2.3", "169.254.169.254", "::ffff:127.0.0.1"} {
		t.Run(blocked, func(t *testing.T) {
			lookup := func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP(blocked)}}, nil
			}
			dial := func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("must validate the complete answer before dialing")
				return nil, nil
			}
			_, err := dialGuard(time.Second, lookup, dial)(context.Background(), "tcp", "allowed.example:443")
			if !errors.Is(err, ErrFetchBlocked) {
				t.Fatalf("expected policy rejection, got %v", err)
			}
		})
	}
}

func TestDialGuardRespectsAddressFamilyAndLiteral(t *testing.T) {
	for _, tc := range []struct {
		network, address, want string
		lookup                 bool
	}{
		{"tcp4", "allowed.example:80", "8.8.8.8:80", true},
		{"tcp6", "allowed.example:80", "[2001:4860:4860::8888]:80", true},
		{"tcp", "8.8.8.8:443", "8.8.8.8:443", false},
	} {
		t.Run(tc.network+tc.address, func(t *testing.T) {
			lookups := 0
			lookup := func(context.Context, string) ([]net.IPAddr, error) {
				lookups++
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("2001:4860:4860::8888")}}, nil
			}
			stopped := errors.New("fixture dial stopped")
			dial := func(_ context.Context, _ string, address string) (net.Conn, error) {
				if address != tc.want {
					t.Fatalf("dialed %q want %q", address, tc.want)
				}
				return nil, stopped
			}
			_, err := dialGuard(time.Second, lookup, dial)(context.Background(), tc.network, tc.address)
			if !errors.Is(err, stopped) || (lookups == 1) != tc.lookup {
				t.Fatalf("err=%v lookups=%d", err, lookups)
			}
		})
	}
}

func TestDialGuardHasOneDeadlineAcrossResolutionAndFallback(t *testing.T) {
	var lookupDeadline time.Time
	lookup := func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		lookupDeadline, _ = ctx.Deadline()
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("1.1.1.1")}}, nil
	}
	attempts := 0
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		attempts++
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(lookupDeadline) {
			t.Fatal("attempt extends the overall DNS/connect deadline")
		}
		if attempts == 1 && !deadline.Before(lookupDeadline) {
			t.Fatal("first address consumed the whole fallback budget")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, err := dialGuard(200*time.Millisecond, lookup, dial)(context.Background(), "tcp", "allowed.example:443")
	if !errors.Is(err, context.DeadlineExceeded) || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestPinnedDialKeepsOriginalHTTPSHostAcrossRedirect(t *testing.T) {
	observations := make(chan [3]string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observations <- [3]string{r.Host, r.TLS.ServerName, r.Header.Get("Authorization")}
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "https://example.com/final", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "verified external material")
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	guard := dialGuard(time.Second, func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "example.com" {
			return nil, errors.New("unexpected DNS name")
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "8.8.8.8:443" {
			return nil, errors.New("unvalidated target passed to socket dial")
		}
		// Only the fixture socket redirects the pinned public IP to our TLS server.
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	})
	transport := &http.Transport{DialContext: guard, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	policy := DefaultFetchPolicy([]string{"example.com"})
	client, err := ControlledFetcher(policy, &http.Client{Transport: transport, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	content, err := Fetch(context.Background(), client, policy, "https://example.com/start")
	if err != nil {
		t.Fatal(err)
	}
	if content.Text != "verified external material" {
		t.Fatalf("wrong body %q", content.Text)
	}
	for range 2 {
		select {
		case got := <-observations:
			if got != [3]string{"example.com", "example.com", ""} {
				t.Fatalf("Host/SNI/credentials = %v", got)
			}
		default:
			t.Fatal("both TLS requests must complete")
		}
	}
}
