// Package ipcountry finds the country (ISO 3166-1 alpha-2 code) of a
// public address with public lookup services. Like pubip, the caller
// supplies dial and resolve functions, so the lookup leaves through the
// interface the address was detected on, in the same family.
//
// Service entries:
//
//	trace:https://host/cdn-cgi/trace  Cloudflare trace: the requester's own
//	                                  country (loc=), used only when its
//	                                  ip= is the address asked about
//	https://host/path/{ip}            the country of {ip}: a bare code or a
//	                                  JSON object with a country field
package ipcountry

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/internal/pubip"
)

// DefaultServices is tried in order; the first valid answer wins.
var DefaultServices = []string{
	// Same request kind as public-address detection; no third party sees
	// an address it was not already serving.
	"trace:https://www.cloudflare.com/cdn-cgi/trace",
	"https://api.country.is/{ip}",
	"https://get.geojs.io/v1/ip/country/{ip}",
	"https://ipinfo.io/{ip}/country",
}

// Services is DefaultServices unless HY2SCALE_COUNTRY_SERVICES (comma
// separated entries) overrides it, e.g. for an offline test bench.
func Services() []string {
	if v := strings.TrimSpace(os.Getenv("HY2SCALE_COUNTRY_SERVICES")); v == "off" {
		return nil
	} else if v != "" {
		var out []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return DefaultServices
}

// PerServiceTimeout bounds one service entry.
var PerServiceTimeout = 5 * time.Second

const bodyLimit = 2048

// Lookup returns the country code of ip.
func Lookup(ctx context.Context, family int, ip netip.Addr, dial pubip.DialFunc, resolve pubip.ResolveFunc) (string, error) {
	if !ip.IsValid() {
		return "", errors.New("no address")
	}
	client := newClient(family, dial, resolve)
	defer client.CloseIdleConnections()
	var lastErr error = errors.New("no country service")
	for _, svc := range Services() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		code, err := query(ctx, client, svc, ip)
		if err == nil {
			return code, nil
		}
		lastErr = fmt.Errorf("%s: %w", svc, err)
	}
	return "", lastErr
}

func query(ctx context.Context, client *http.Client, svc string, ip netip.Addr) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, PerServiceTimeout)
	defer cancel()
	trace := strings.HasPrefix(svc, "trace:")
	u := strings.TrimPrefix(svc, "trace:")
	if !trace {
		u = strings.ReplaceAll(u, "{ip}", ip.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hy2scale-country")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil && len(body) == 0 {
		return "", err
	}
	if trace {
		return ParseTrace(body, ip)
	}
	return ParseBody(body)
}

// ParseBody reads a bare country code or a JSON object carrying one under
// country / country_code / countryCode.
func ParseBody(body []byte) (string, error) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '{' {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			return "", err
		}
		for _, k := range []string{"country", "country_code", "countryCode"} {
			if s, ok := m[k].(string); ok {
				return validCode(s)
			}
		}
		return "", errors.New("no country in response")
	}
	return validCode(string(body))
}

// ParseTrace reads loc= from a Cloudflare trace body, only when its ip=
// is the address asked about (the request left with that address).
func ParseTrace(body []byte, ip netip.Addr) (string, error) {
	var seen netip.Addr
	var loc string
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			if a, err := netip.ParseAddr(v); err == nil {
				seen = a.Unmap()
			}
		case "loc":
			loc = v
		}
	}
	if seen != ip.Unmap() {
		return "", errors.New("trace saw another address")
	}
	return validCode(loc)
}

// validCode accepts a two-letter code, upper-cased; XX / T1 style
// placeholders ("unknown", "Tor") are rejected.
func validCode(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' || s == "XX" || s == "ZZ" {
		return "", fmt.Errorf("not a country code: %q", s)
	}
	return s, nil
}

// newClient dials through dial in the given family, resolving with resolve.
func newClient(family int, dial pubip.DialFunc, resolve pubip.ResolveFunc) *http.Client {
	tcp, ipNet := "tcp4", "ip4"
	if family == 6 {
		tcp, ipNet = "tcp6", "ip6"
	}
	tr := &http.Transport{
		Proxy: nil, // never honour environment proxies: the lookup must leave via dial
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var addrs []netip.Addr
			if a, err := netip.ParseAddr(host); err == nil {
				addrs = []netip.Addr{a}
			} else if addrs, err = resolve(ctx, ipNet, host); err != nil {
				return nil, err
			}
			var lastErr error = errors.New("no address for " + host)
			for i, a := range addrs {
				if i == 2 {
					break
				}
				c, err := dial(ctx, tcp, net.JoinHostPort(a.Unmap().String(), port))
				if err == nil {
					return c, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 16 << 10,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}
