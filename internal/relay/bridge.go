package relay

import (
	"strings"
	"sync"
	"time"
)

// Pre-1.3.10 nodes tagged exit streams with "#bridge=<id>" and, after losing
// a QUIC connection, asked for the same id again to splice a new stream onto
// the old target connection ("rebind"). The bytes in flight on the dead
// stream were lost, so a rebound stream carried a silently damaged byte
// stream, and both ends held connections for up to 90 s waiting for it.
// 1.3.10 recovers end-to-end instead (internal/msess): tags from old peers
// are stripped and every rebind is refused, so the old peer closes the
// connection rather than continuing it with a gap.

const bridgeRebindAddr = "_relay_rebind_"

// ParseBridgeAddr strips an old peer's bridge tag.
// Returns (targetAddr, bridgeID, isBridged).
func ParseBridgeAddr(addr string) (string, string, bool) {
	idx := strings.Index(addr, "#bridge=")
	if idx < 0 {
		return addr, "", false
	}
	return addr[:idx], addr[idx+8:], true
}

// bridgeTags tracks the tags of via streams relayed for old peers. A via
// rebind reuses the tag of its (dead) stream on a request that otherwise
// looks new; dialing it would feed mid-stream bytes to a fresh target
// connection. Tags stay known while their stream lives and for
// bridgeTagLinger after it ends (old peers gave up rebinding after 90 s).
type bridgeTags struct {
	mu    sync.Mutex
	live  map[string]int
	ended map[string]time.Time
	swept time.Time
}

const bridgeTagLinger = 3 * time.Minute

func newBridgeTags() *bridgeTags {
	return &bridgeTags{live: map[string]int{}, ended: map[string]time.Time{}}
}

// open registers tag for a new stream. false = the tag is (recently) in use:
// this is a rebind and must be refused.
func (b *bridgeTags) open(tag string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if now.Sub(b.swept) > 30*time.Second {
		for k, t := range b.ended {
			if now.Sub(t) > bridgeTagLinger {
				delete(b.ended, k)
			}
		}
		b.swept = now
	}
	if b.live[tag] > 0 {
		return false
	}
	if t, ok := b.ended[tag]; ok && now.Sub(t) <= bridgeTagLinger {
		return false
	}
	b.live[tag]++
	return true
}

func (b *bridgeTags) close(tag string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.live[tag]--; b.live[tag] <= 0 {
		delete(b.live, tag)
		b.ended[tag] = time.Now()
	}
}

func (b *bridgeTags) counts() (live, ended int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.live), len(b.ended)
}
