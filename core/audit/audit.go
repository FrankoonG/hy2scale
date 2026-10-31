// Package audit carries per-flow audit records from the data path to a
// sink (the node's audit store). A flow is begun where traffic enters the
// node, annotated by the layers it passes (routing, exit, egress) and ended
// when it closes; long flows also emit interim records.
//
// Recording is off by default. While it is off, Begin returns nil and every
// Flow method is a no-op on a nil Flow, so the data path pays one atomic
// load per flow and nothing per byte.
package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Record is one audit record. Field names are part of the API.
type Record struct {
	ID          string `json:"id"`
	Start       int64  `json:"start"` // unix ms
	End         int64  `json:"end"`   // unix ms
	Partial     bool   `json:"partial,omitempty"`
	Proto       string `json:"proto,omitempty"`   // tcp | udp
	Inbound     string `json:"inbound,omitempty"` // socks5 http ss hy2 ikev2 l2tp openvpn wireguard rules access relay
	User        string `json:"user,omitempty"`
	Src         string `json:"src,omitempty"`
	SrcNode     string `json:"src_node,omitempty"`
	InIface     string `json:"in_iface,omitempty"` // inbound interface (forwarded traffic)
	SrcMAC      string `json:"src_mac,omitempty"`  // source MAC (forwarded traffic)
	Dst         string `json:"dst,omitempty"`
	DstHost     string `json:"dst_host,omitempty"`
	DstIP       string `json:"dst_ip,omitempty"`
	Rule        string `json:"rule,omitempty"`     // policy chain ("set/rule > set/rule")
	Policy      string `json:"policy,omitempty"`   // the policy set that gave the terminal (id)
	Terminal    string `json:"terminal,omitempty"` // deny | exit | iface | default
	Reason      string `json:"reason,omitempty"`   // why: rule | default | direct | exit | depth | missing
	Exit        string `json:"exit,omitempty"`     // direct | node | blocked
	ExitPath    string `json:"exit_path,omitempty"`
	ExitMode    string `json:"exit_mode,omitempty"`
	EgressIface string `json:"egress_iface,omitempty"`
	EgressSrc   string `json:"egress_src,omitempty"`
	Up          int64  `json:"up"`               // client → target
	Down        int64  `json:"down"`             // target → client
	Result      string `json:"result,omitempty"` // ok | error | refused | blocked
	Error       string `json:"error,omitempty"`
}

// Inbound kinds.
const (
	InSOCKS5    = "socks5"
	InHTTP      = "http"
	InSS        = "ss"
	InHy2       = "hy2"
	InIKEv2     = "ikev2"
	InL2TP      = "l2tp"
	InOpenVPN   = "openvpn"
	InWireGuard = "wireguard"
	InRules     = "rules"
	InAccess    = "access"
	InRelay     = "relay"
)

// Results.
const (
	ResultOK      = "ok"
	ResultError   = "error"
	ResultRefused = "refused"
	ResultBlocked = "blocked"
)

// InterimEvery: an open flow gets an interim (partial) record this often.
const InterimEvery = 10 * time.Minute

const maxErrLen = 200

var (
	enabled atomic.Bool
	sinkFn  atomic.Pointer[func(*Record)]
	open    sync.Map // *Flow → struct{}
	openN   atomic.Int64
	seq     atomic.Uint64
	idPfx   = func() string {
		var b [4]byte
		rand.Read(b[:])
		return hex.EncodeToString(b[:])
	}()
)

// SetEnabled turns recording on or off. Flows begun while it was on still
// end, but their records are only delivered while it is on.
func SetEnabled(on bool) { enabled.Store(on) }

// Enabled reports whether flows are recorded.
func Enabled() bool { return enabled.Load() }

// SetSink installs the receiver of finished and interim records. It must
// not block (queue and drop instead).
func SetSink(fn func(*Record)) {
	if fn == nil {
		sinkFn.Store(nil)
		return
	}
	sinkFn.Store(&fn)
}

func emit(r *Record) {
	if !enabled.Load() {
		return
	}
	if p := sinkFn.Load(); p != nil {
		(*p)(r)
	}
}

// OpenFlows: flows begun and not yet ended (diagnostics).
func OpenFlows() int { return int(openN.Load()) }

func newID() string {
	return idPfx + "-" + strconv.FormatUint(seq.Add(1), 36)
}

// Flow is one flow being recorded. All methods are safe on a nil Flow.
type Flow struct {
	mu       sync.Mutex
	rec      Record
	up, down atomic.Int64
	done     atomic.Bool
	lastEmit atomic.Int64 // unix ms of the start or the last interim record
}

// Begin starts recording a flow entering by inbound over proto ("tcp" or
// "udp"); nil while recording is off.
func Begin(inbound, proto string) *Flow {
	if !enabled.Load() {
		return nil
	}
	now := time.Now().UnixMilli()
	f := &Flow{rec: Record{ID: newID(), Start: now, Inbound: inbound, Proto: proto}}
	f.lastEmit.Store(now)
	open.Store(f, struct{}{})
	openN.Add(1)
	return f
}

// Set changes the record under the flow's lock (nil-safe).
func (f *Flow) Set(fn func(r *Record)) {
	if f == nil {
		return
	}
	f.mu.Lock()
	fn(&f.rec)
	f.mu.Unlock()
}

// SetUser records the user.
func (f *Flow) SetUser(u string) {
	if f == nil || u == "" {
		return
	}
	f.Set(func(r *Record) { r.User = u })
}

// SetSrc records the client address (and the peer it came from).
func (f *Flow) SetSrc(src, node string) {
	if f == nil {
		return
	}
	f.Set(func(r *Record) {
		if src != "" {
			r.Src = src
		}
		if node != "" {
			r.SrcNode = node
		}
	})
}

// SetDst records the destination as requested; a host name in it is also
// the destination host.
func (f *Flow) SetDst(dst string) {
	if f == nil || dst == "" {
		return
	}
	f.Set(func(r *Record) {
		r.Dst = dst
		if h, _, err := net.SplitHostPort(dst); err == nil && h != "" && net.ParseIP(h) == nil {
			r.DstHost = h
		}
	})
}

// SetDstHost records a destination host name (requested or sniffed).
func (f *Flow) SetDstHost(h string) {
	if f == nil || h == "" {
		return
	}
	f.Set(func(r *Record) { r.DstHost = h })
}

// SetRule records the routing rule that matched.
func (f *Flow) SetRule(name string) {
	if f == nil || name == "" {
		return
	}
	f.Set(func(r *Record) { r.Rule = name })
}

// SetPolicy records a policy decision: the chain text (also the rule),
// the policy set that gave the terminal ("" = none) and the terminal type.
func (f *Flow) SetPolicy(chain, policy, terminal string) {
	if f == nil {
		return
	}
	f.Set(func(r *Record) {
		r.Rule, r.Policy, r.Terminal = chain, policy, terminal
	})
}

// SetReason records why the terminal was chosen (policy.Reason*).
func (f *Flow) SetReason(reason string) {
	if f == nil || reason == "" {
		return
	}
	f.Set(func(r *Record) { r.Reason = reason })
}

// SetLink records the inbound interface and source MAC of a forwarded flow.
func (f *Flow) SetLink(iface, mac string) {
	if f == nil || (iface == "" && mac == "") {
		return
	}
	f.Set(func(r *Record) {
		if iface != "" {
			r.InIface = iface
		}
		if mac != "" {
			r.SrcMAC = mac
		}
	})
}

// SetExitNode records an exit through other nodes.
func (f *Flow) SetExitNode(path, mode string) {
	if f == nil {
		return
	}
	f.Set(func(r *Record) {
		r.Exit = "node"
		if path != "" {
			r.ExitPath = path
		}
		r.ExitMode = mode
	})
}

// SetExitPath refines the exit path (the path a failover race picked).
func (f *Flow) SetExitPath(path string) {
	if f == nil || path == "" {
		return
	}
	f.Set(func(r *Record) {
		if r.Exit == "node" {
			r.ExitPath = path
		}
	})
}

// SetDirect records a dial made from this node: the address actually
// dialled and the local interface and source address used.
func (f *Flow) SetDirect(dstIP, iface, src string) {
	if f == nil {
		return
	}
	f.Set(func(r *Record) {
		r.Exit = "direct"
		r.ExitPath, r.ExitMode = "", ""
		if dstIP != "" {
			r.DstIP = dstIP
		}
		r.EgressIface, r.EgressSrc = iface, src
	})
}

// SetEgress records the local leg (interface, source address) without
// changing the exit kind.
func (f *Flow) SetEgress(iface, src string) {
	if f == nil {
		return
	}
	f.Set(func(r *Record) {
		if iface != "" {
			r.EgressIface = iface
		}
		if src != "" {
			r.EgressSrc = src
		}
	})
}

// Block marks the flow refused by a restriction.
func (f *Flow) Block(why string) {
	if f == nil {
		return
	}
	f.Set(func(r *Record) {
		r.Exit, r.Result, r.Error = "blocked", ResultBlocked, shortErr(why)
		r.ExitPath, r.ExitMode = "", ""
	})
}

// Fail sets the result (refused, error, …) and a short reason; the first
// one set stays.
func (f *Flow) Fail(result string, err error) {
	if f == nil {
		return
	}
	f.Set(func(r *Record) {
		if r.Result != "" {
			return
		}
		r.Result = result
		if err != nil {
			r.Error = shortErr(err.Error())
		}
	})
}

// Add counts bytes: up from the client, down to it.
func (f *Flow) Add(up, down int64) {
	if f == nil {
		return
	}
	if up != 0 {
		f.up.Add(up)
	}
	if down != 0 {
		f.down.Add(down)
	}
}

// End finishes the flow: err (a failure to set the flow up) marks it an
// error unless a result is already set. Only the first call counts.
func (f *Flow) End(err error) {
	if f == nil || f.done.Swap(true) {
		return
	}
	open.Delete(f)
	openN.Add(-1)
	f.mu.Lock()
	r := f.rec
	f.mu.Unlock()
	r.End = time.Now().UnixMilli()
	r.Up, r.Down = f.up.Load(), f.down.Load()
	if r.Result == "" {
		if err != nil {
			r.Result = ResultError
			r.Error = shortErr(err.Error())
		} else {
			r.Result = ResultOK
		}
	}
	emit(&r)
}

// Done reports whether End was called (or f is nil).
func (f *Flow) Done() bool { return f == nil || f.done.Load() }

// Snapshot returns the record as it stands (diagnostics, tests).
func (f *Flow) Snapshot() Record {
	if f == nil {
		return Record{}
	}
	f.mu.Lock()
	r := f.rec
	f.mu.Unlock()
	r.Up, r.Down = f.up.Load(), f.down.Load()
	return r
}

// Interim emits a partial record for every open flow whose start or last
// interim record is at least every ago. The storage calls it about once a
// minute.
func Interim(now time.Time, every time.Duration) int {
	if !enabled.Load() {
		return 0
	}
	ms := now.UnixMilli()
	n := 0
	open.Range(func(k, _ any) bool {
		f := k.(*Flow)
		last := f.lastEmit.Load()
		if ms-last < every.Milliseconds() || f.done.Load() {
			return true
		}
		if !f.lastEmit.CompareAndSwap(last, ms) {
			return true
		}
		r := f.Snapshot()
		r.End, r.Partial = ms, true
		if r.Result == "" {
			r.Result = ResultOK
		}
		emit(&r)
		n++
		return true
	})
	return n
}

func shortErr(s string) string {
	if len(s) > maxErrLen {
		return s[:maxErrLen]
	}
	return s
}

type ctxKey struct{}

// NewContext returns ctx carrying f (ctx itself when f is nil).
func NewContext(ctx context.Context, f *Flow) context.Context {
	if f == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, f)
}

// FromContext returns the flow ctx carries, or nil.
func FromContext(ctx context.Context) *Flow {
	if ctx == nil || !enabled.Load() {
		return nil
	}
	f, _ := ctx.Value(ctxKey{}).(*Flow)
	return f
}

// Carrier is a connection that carries the identity of the client it came
// from (node access by user: the relay layer records its flows as that
// user's).
type Carrier interface {
	AuditIdentity() (inbound, user string)
}

// IdentityOf returns the identity a connection carries, unwrapping
// net.Conn wrappers that expose Unwrap() net.Conn.
func IdentityOf(c net.Conn) (inbound, user string, ok bool) {
	for i := 0; c != nil && i < 8; i++ {
		if ca, isCa := c.(Carrier); isCa {
			in, u := ca.AuditIdentity()
			return in, u, in != ""
		}
		w, isW := c.(interface{ Unwrap() net.Conn })
		if !isW {
			break
		}
		c = w.Unwrap()
	}
	return "", "", false
}

// ---------------------------------------------------------------- conns

// Track wraps c, a connection towards the flow's target: bytes read from it
// count as down, bytes written as up, and Close ends the flow. client: c is
// the client side instead (reads are up). With f nil, c is returned as is.
func Track(f *Flow, c net.Conn, client bool) net.Conn {
	if f == nil || c == nil {
		return c
	}
	t := tracked{Conn: c, f: f, client: client}
	if _, ok := c.(interface{ CloseWrite() error }); ok {
		return &trackedCW{t}
	}
	return &t
}

type tracked struct {
	net.Conn
	f      *Flow
	client bool
}

func (c *tracked) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		if c.client {
			c.f.up.Add(int64(n))
		} else {
			c.f.down.Add(int64(n))
		}
	}
	return n, err
}

func (c *tracked) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		if c.client {
			c.f.down.Add(int64(n))
		} else {
			c.f.up.Add(int64(n))
		}
	}
	return n, err
}

func (c *tracked) Close() error {
	err := c.Conn.Close()
	c.f.End(nil)
	return err
}

// Unwrap: the wrapped connection.
func (c *tracked) Unwrap() net.Conn { return c.Conn }

// NoHalfClose passes the wrapped connection's half-close capability on.
func (c *tracked) NoHalfClose() bool {
	nh, ok := c.Conn.(interface{ NoHalfClose() bool })
	return ok && nh.NoHalfClose()
}

type trackedCW struct{ tracked }

func (c *trackedCW) CloseWrite() error {
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}

// ---------------------------------------------------------------- egress

var ifaceOf atomic.Pointer[func(ip net.IP) string]

// SetIfaceResolver installs the lookup of the local interface that owns a
// source address (the node's interface table).
func SetIfaceResolver(fn func(ip net.IP) string) {
	if fn == nil {
		ifaceOf.Store(nil)
		return
	}
	ifaceOf.Store(&fn)
}

// IfaceOf: the local interface owning ip ("" = unknown).
func IfaceOf(ip net.IP) string {
	if p := ifaceOf.Load(); p != nil && ip != nil {
		return (*p)(ip)
	}
	return ""
}

// AddrIP: the IP of a TCP/UDP address ("" = none).
func AddrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.IP
	case *net.UDPAddr:
		return v.IP
	case nil:
		return nil
	}
	if h, _, err := net.SplitHostPort(a.String()); err == nil {
		return net.ParseIP(h)
	}
	return nil
}

// MarkDirect records c, a connection this node dialled itself, as the
// flow's direct exit: its remote IP, local interface and source address.
func MarkDirect(f *Flow, c net.Conn) {
	if f == nil || c == nil {
		return
	}
	dst, src := AddrIP(c.RemoteAddr()), AddrIP(c.LocalAddr())
	ds, ss := "", ""
	if dst != nil {
		ds = dst.String()
	}
	if src != nil && !src.IsUnspecified() {
		ss = src.String()
	}
	f.SetDirect(ds, IfaceOf(src), ss)
}
