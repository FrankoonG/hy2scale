package tools

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/proto"
)

// Speed tests over several exit paths, chosen as for a user's exit:
//
//   - quality: every path is dialled at once and the test runs over the one
//     that answers first (the path a quality exit would favour now);
//   - aggregate: the test runs over every path at the same time and the
//     rates add up, with the share of each path.
//
// The tests are ordinary speed streams to each path's last node, so any
// node that serves speed tests can be the target.

// SpeedQuality picks the first path to answer and tests over it.
func SpeedQuality(ctx context.Context, dial ChainDialer, paths [][]string, dir string, secs int, emit Emit) error {
	type res struct {
		c    net.Conn
		path []string
		err  error
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ch := make(chan res, len(paths))
	for _, p := range paths {
		go func(p []string) {
			c, err := dial(rctx, p, proto.StreamSpeed)
			ch <- res{c, p, err}
		}(p)
	}
	var win res
	var errs []string
	for range paths {
		r := <-ch
		if r.err != nil {
			errs = append(errs, strings.Join(r.path, "/")+": "+r.err.Error())
			continue
		}
		if win.c == nil {
			win = r
			cancel() // the rest give up
			continue
		}
		r.c.Close()
	}
	if win.c == nil {
		return errors.New(strings.Join(errs, "; "))
	}
	if emit(Event{"type": "path", "path": strings.Join(win.path, "/")}) != nil {
		win.c.Close()
		return nil
	}
	once := win.c
	d := func(ctx context.Context, addr string) (net.Conn, error) {
		if addr == proto.StreamSpeed && once != nil {
			c := once
			once = nil
			return c, nil
		}
		return dial(ctx, win.path, addr)
	}
	return SpeedNode(ctx, d, dir, secs, emit)
}

// pathRun is one path of an aggregate test.
type pathRun struct {
	path  string
	bytes int64
	done  bool
	err   string
}

// SpeedAggregate tests over every path at once; progress and the result
// carry the total and each path.
func SpeedAggregate(ctx context.Context, dial ChainDialer, paths [][]string, dir string, secs int, emit Emit) error {
	secs = min(max(secs, 1), MaxSpeedSeconds)
	var mu sync.Mutex
	runs := make([]*pathRun, len(paths))
	var wg sync.WaitGroup
	start := time.Now()
	for i, p := range paths {
		r := &pathRun{path: strings.Join(p, "/")}
		runs[i] = r
		wg.Add(1)
		go func(p []string) {
			defer wg.Done()
			d := func(ctx context.Context, addr string) (net.Conn, error) { return dial(ctx, p, addr) }
			err := SpeedNode(ctx, d, dir, secs, func(ev Event) error {
				if b, ok := ev["bytes"].(int64); ok {
					mu.Lock()
					r.bytes = b
					mu.Unlock()
				}
				return nil
			})
			mu.Lock()
			r.done = true
			if err != nil {
				r.err = err.Error()
			}
			mu.Unlock()
		}(p)
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()

	snapshot := func() (int64, []Event) {
		mu.Lock()
		defer mu.Unlock()
		var total int64
		per := make([]Event, len(runs))
		for i, r := range runs {
			total += r.bytes
			e := Event{"path": r.path, "bytes": r.bytes, "mbps": mbps(r.bytes, time.Since(start))}
			if r.err != "" {
				e["error"] = r.err
			}
			per[i] = e
		}
		return total, per
	}
	t := time.NewTicker(speedTick)
	defer t.Stop()
	var lastAt time.Duration
	var lastBytes int64
	for {
		select {
		case <-ctx.Done():
			<-finished
			return nil
		case <-finished:
			total, per := snapshot()
			mu.Lock()
			failed := 0
			for _, r := range runs {
				if r.err != "" {
					failed++
				}
			}
			mu.Unlock()
			if failed == len(runs) {
				msgs := make([]string, 0, len(per))
				for _, e := range per {
					msgs = append(msgs, e["path"].(string)+": "+e["error"].(string))
				}
				return errors.New(strings.Join(msgs, "; "))
			}
			el := time.Since(start)
			if el > time.Duration(secs)*time.Second {
				el = time.Duration(secs) * time.Second
			}
			for _, e := range per {
				e["mbps"] = mbps(e["bytes"].(int64), el)
			}
			return emit(Event{"type": "done", "bytes": total, "seconds": round3(el.Seconds()), "mbps": mbps(total, el), "paths": per})
		case <-t.C:
			total, per := snapshot()
			el := time.Since(start)
			cur := mbps(total-lastBytes, el-lastAt)
			lastAt, lastBytes = el, total
			if emit(Event{"type": "progress", "elapsed_ms": el.Milliseconds(), "bytes": total,
				"mbps": mbps(total, el), "cur_mbps": cur, "paths": per}) != nil {
				<-finished
				return nil
			}
		}
	}
}
