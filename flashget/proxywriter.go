package flashget

import (
	"io"
	"time"

	"code.cloudfoundry.org/bytefmt"
)

// ProxyWriter record download bytes
type ProxyWriter struct {
	W         io.Writer
	written   int
	createdAt time.Time
}

func NewProxyWriter(wr io.Writer) *ProxyWriter {
	return &ProxyWriter{
		W:         wr,
		createdAt: time.Now(),
	}
}

func (p *ProxyWriter) Write(data []byte) (n int, err error) {
	n, err = p.W.Write(data)
	p.written += n
	return
}

func (p *ProxyWriter) Written() int {
	return p.written
}

func (p *ProxyWriter) HumanSpeed() string {
	byteps := uint64(float64(p.written) / time.Since(p.createdAt).Seconds())
	return bytefmt.ByteSize(byteps) + "/s"
}
