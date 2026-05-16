package app

import (
	"context"
	"time"

	"github.com/FrankoonG/hy2scale/internal/history"
)

// runHistoryRecorder ticks every 5 s, samples each peer's latest
// latency/online state plus its current tx/rx rate, and folds the
// observation into the bucket store. Once per minute it triggers an
// atomic snapshot to dataDir/history.json so the rings survive process
// restart. Old data beyond the retention horizon is pruned each tick.
func (a *App) runHistoryRecorder(ctx context.Context) {
	if a.hist == nil {
		return
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	flush := time.NewTicker(60 * time.Second)
	defer flush.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = a.hist.Persist()
			return
		case now := <-tick.C:
			a.recordOnce(now)
		case now := <-flush.C:
			a.hist.GC(now)
			a.hist.MaybePersist(now)
		}
	}
}

// recordOnce builds a Sample per known peer and feeds them to the store.
func (a *App) recordOnce(now time.Time) {
	snaps := a.node.LatencySnapshots()
	rates := a.node.PeerRates()
	if len(snaps) == 0 && len(rates) == 0 {
		return
	}
	samples := make([]history.Sample, 0, len(snaps))
	seen := make(map[string]struct{}, len(snaps))
	for _, sn := range snaps {
		r := rates[sn.Name]
		// peer rate is bytes/sec (1-second delta); the field is named *Rate
		// in PeerTraffic. Surface it directly as bps.
		samples = append(samples, history.Sample{
			Peer:      sn.Name,
			At:        now,
			LatencyMs: sn.LatencyMs,
			TxBps:     int64(r.TxRate),
			RxBps:     int64(r.RxRate),
		})
		seen[sn.Name] = struct{}{}
	}
	// Edge case: a peer has rate data but disappeared from the snapshot
	// (e.g., torn down between calls). Skip it — the next tick handles
	// the new topology.
	a.hist.Record(samples)
	a.hist.Forget(seen)
}
