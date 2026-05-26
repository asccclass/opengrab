package cmdrunner

import "sync"

type RingBuffer struct {
	mu  sync.RWMutex
	max int
	buf []byte
}

func NewRingBuffer(max int) *RingBuffer {
	if max <= 0 {
		max = 64 * 1024
	}
	return &RingBuffer{
		max: max,
		buf: make([]byte, 0, max),
	}
}

func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	if n == 0 {
		return 0, nil
	}

	if n >= r.max {
		r.buf = append(r.buf[:0], p[n-r.max:]...)
		return n, nil
	}

	total := len(r.buf) + n
	if total > r.max {
		drop := total - r.max
		if drop >= len(r.buf) {
			r.buf = r.buf[:0]
		} else {
			copy(r.buf, r.buf[drop:])
			r.buf = r.buf[:len(r.buf)-drop]
		}
	}

	r.buf = append(r.buf, p...)
	return n, nil
}

func (r *RingBuffer) Bytes() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

func (r *RingBuffer) String() string {
	return string(r.Bytes())
}

func (r *RingBuffer) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = r.buf[:0]
}
