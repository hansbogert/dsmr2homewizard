package dsmr

import (
	"bufio"
	"io"
	"strings"
)

// Reader frames telegrams from a byte stream (serial port, TCP socket, stdin).
type Reader struct {
	r *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 4096)}
}

// Next returns the next complete raw telegram. Bytes before the first '/' are discarded, so
// starting mid-telegram just skips to the next one.
func (r *Reader) Next() (string, error) {
	var b strings.Builder
	started := false
	for {
		line, err := r.r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if !started {
			i := strings.IndexByte(line, '/')
			if i < 0 {
				continue
			}
			line = line[i:]
			started = true
		} else if strings.HasPrefix(line, "/") {
			// A new header before '!' means the previous telegram was truncated.
			b.Reset()
		}
		b.WriteString(line)
		if strings.HasPrefix(line, "!") {
			return b.String(), nil
		}
		if b.Len() > 64*1024 {
			b.Reset()
			started = false
		}
	}
}
