package pubip

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// httpBodyLimit caps how much of a response body is read.
const httpBodyLimit = 512

func (p *prober) probeHTTP(ctx context.Context, rawURL string) (netip.Addr, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return netip.Addr{}, err
	}
	if u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return netip.Addr{}, fmt.Errorf("bad http server entry")
	}
	tcp := "tcp" + p.suffix()
	tr := &http.Transport{
		Proxy: nil, // never honour environment proxies: the probe must leave via dial
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			addrs, err := p.lookup(ctx, host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, a := range addrs {
				c, err := p.dial(ctx, tcp, net.JoinHostPort(a.String(), port))
				if err == nil {
					return c, nil
				}
				lastErr = err
				if ctx.Err() != nil {
					break
				}
			}
			return nil, lastErr
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 16 << 10,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return netip.Addr{}, err
	}
	req.Header.Set("User-Agent", "hy2scale-pubip")
	req.Header.Set("Accept", "text/plain, */*")
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, ctxErrOr(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("http status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpBodyLimit))
	if err != nil && len(body) == 0 {
		return netip.Addr{}, ctxErrOr(ctx, err)
	}
	ip, err := ParseIPBody(body)
	if err != nil {
		return netip.Addr{}, err
	}
	if err := p.validAnswer(ip); err != nil {
		return netip.Addr{}, err
	}
	return ip.Unmap(), nil
}

// ParseIPBody extracts an IP address from an HTTP "what is my IP" response
// body: either a bare address (surrounding whitespace tolerated) or a
// key=value listing with an "ip=" line, as served by Cloudflare's
// /cdn-cgi/trace.
func ParseIPBody(body []byte) (netip.Addr, error) {
	s := strings.TrimSpace(string(body))
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), nil
	}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "ip" {
			continue
		}
		if a, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
			return a.Unmap(), nil
		}
	}
	return netip.Addr{}, errors.New("no IP address in response body")
}
