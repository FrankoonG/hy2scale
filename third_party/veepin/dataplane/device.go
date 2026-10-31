package dataplane

// Device is the packet device a server's data path reads and writes whole IP
// packets on. *TUN satisfies it; an embedder may hand a server a userspace
// device instead of letting it open /dev/net/tun. (hy2scale patch P1)
type Device interface {
	Read(buf []byte) (int, error)
	Write(pkt []byte) (int, error)
	Close() error
	Name() string
}

// BatchReader is optionally implemented by a userspace Device: ReadBatch
// blocks for the first packet and returns at once with whatever else is
// queued, so the pump can encapsulate a burst and flush it with one batched
// send. (hy2scale P6)
type BatchReader interface {
	ReadBatch(bufs [][]byte, sizes []int) (int, error)
}

var _ Device = (*TUN)(nil)
