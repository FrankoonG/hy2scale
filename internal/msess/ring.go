package msess

// ring is a FIFO byte queue in one circular buffer. The session's send and
// in-order receive queues slide by up to Window per round trip; as plain
// slices every refill re-allocated and copied the whole live window.
type ring struct {
	buf  []byte
	head int // index of the first byte
	n    int // bytes held
}

const ringMin = 64 << 10

func (r *ring) Len() int { return r.n }

// push appends p; the buffer doubles up to limit (further only as needed).
func (r *ring) push(p []byte, limit int) {
	if r.n+len(p) > len(r.buf) {
		c := 2 * len(r.buf)
		if c > limit {
			c = limit
		}
		if c < ringMin {
			c = ringMin
		}
		if c < r.n+len(p) {
			c = r.n + len(p)
		}
		nb := make([]byte, c)
		r.copyOut(nb, 0, r.n)
		r.buf, r.head = nb, 0
	}
	tail := r.head + r.n
	if tail >= len(r.buf) {
		tail -= len(r.buf)
	}
	k := copy(r.buf[tail:], p)
	copy(r.buf, p[k:])
	r.n += len(p)
}

// copyOut copies bytes [off, off+n) of the queue into dst.
func (r *ring) copyOut(dst []byte, off, n int) {
	if n == 0 {
		return
	}
	i := r.head + off
	if i >= len(r.buf) {
		i -= len(r.buf)
	}
	k := copy(dst[:n], r.buf[i:])
	copy(dst[k:n], r.buf)
}

// appendTo appends bytes [off, off+n) of the queue to dst.
func (r *ring) appendTo(dst []byte, off, n int) []byte {
	l := len(dst)
	if cap(dst)-l < n {
		nd := make([]byte, l, l+n)
		copy(nd, dst)
		dst = nd
	}
	dst = dst[:l+n]
	r.copyOut(dst[l:], off, n)
	return dst
}

// read moves up to len(p) bytes from the front into p.
func (r *ring) read(p []byte) int {
	n := r.n
	if n > len(p) {
		n = len(p)
	}
	r.copyOut(p, 0, n)
	r.drop(n)
	return n
}

// drop discards k bytes from the front; an emptied large buffer is freed
// so an idle session holds no window-sized memory.
func (r *ring) drop(k int) {
	r.n -= k
	if r.n == 0 {
		r.head = 0
		if len(r.buf) > ringMin {
			r.buf = nil
		}
		return
	}
	r.head += k
	if r.head >= len(r.buf) {
		r.head -= len(r.buf)
	}
}

func (r *ring) reset() { *r = ring{} }
