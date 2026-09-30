package upstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// MaxSSEEventBytes matches the synchronous JSON response inspection limit.
const MaxSSEEventBytes = 16 << 20

var ErrSSEEventTooLarge = errors.New("upstream SSE event exceeds 16 MiB limit")

// SSEReader preserves SSE bytes except for a missing blank line before an
// explicit event: field following an explicitly named, complete JSON event. The shared framing
// keeps gateway forwarding and recovery probes consistent. Incomplete JSON,
// multiline data and data-only frames are never split speculatively.
type SSEReader struct {
	reader     *bufio.Reader
	pending    []byte
	err        error
	data       []byte
	eventName  string
	eventBytes int
}

func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{reader: bufio.NewReaderSize(r, 32*1024)}
}

func (r *SSEReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		line, err := r.readLine()
		if errors.Is(err, ErrSSEEventTooLarge) {
			r.err = err
			return 0, err
		}
		r.err = err
		if len(line) == 0 {
			return 0, err
		}
		field := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		isEvent := bytes.HasPrefix(field, []byte("event:"))
		repair := isEvent && r.eventName != "" && completeSSEObject(r.data)
		if repair {
			r.data = nil
			r.eventName = ""
			r.eventBytes = 0
		}
		r.eventBytes += len(line)
		if r.eventBytes > MaxSSEEventBytes {
			r.err = ErrSSEEventTooLarge
			return 0, r.err
		}
		if len(field) == 0 {
			r.data = nil
			r.eventName = ""
			r.eventBytes = 0
		} else if isEvent {
			r.eventName = strings.TrimSpace(string(field[6:]))
		}
		if bytes.HasPrefix(field, []byte("data:")) {
			if len(r.data) > 0 {
				r.data = append(r.data, '\n')
			}
			r.data = append(r.data, bytes.TrimPrefix(field[5:], []byte(" "))...)
		}
		if repair {
			r.pending = append([]byte{'\n'}, line...)
		} else {
			r.pending = line
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	if len(r.pending) == 0 {
		r.pending = nil
	}
	return n, nil
}

func (r *SSEReader) readLine() ([]byte, error) {
	var line []byte
	for {
		part, err := r.reader.ReadSlice('\n')
		if len(line)+len(part) > MaxSSEEventBytes {
			return nil, ErrSSEEventTooLarge
		}
		line = append(line, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func completeSSEObject(data []byte) bool {
	data = bytes.TrimSpace(data)
	return len(data) > 0 && data[0] == '{' && json.Valid(data)
}
