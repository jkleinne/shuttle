package engine

import "io"

const (
	outputRecordLimitBytes = 64 * 1024
	statisticsTailBytes    = 64 * 1024
	outputReadBufferBytes  = 32 * 1024
	outputTruncationSuffix = " [truncated]"
)

// captureDelimitedRecords streams nonempty CR, LF, or CRLF-delimited records
// from reader. Each record retains at most outputRecordLimitBytes, but the
// reader continues draining through the delimiter and EOF.
func captureDelimitedRecords(reader io.Reader, onRecord func(string)) error {
	readBuffer := make([]byte, outputReadBufferBytes)
	record := make([]byte, 0, outputReadBufferBytes)
	truncated := false
	previousWasCR := false

	flush := func() {
		if len(record) == 0 && !truncated {
			return
		}
		if onRecord != nil {
			text := string(record)
			if truncated {
				text += outputTruncationSuffix
			}
			onRecord(text)
		}
		record = record[:0]
		truncated = false
	}

	for {
		read, readErr := reader.Read(readBuffer)
		for _, value := range readBuffer[:read] {
			if previousWasCR {
				previousWasCR = false
				if value == '\n' {
					continue
				}
			}
			switch value {
			case '\r':
				flush()
				previousWasCR = true
			case '\n':
				flush()
			default:
				if len(record) < outputRecordLimitBytes {
					record = append(record, value)
				} else {
					truncated = true
				}
			}
		}
		if readErr != nil {
			flush()
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

// tailBuffer is an io.Writer retaining only the last capacity bytes written.
// The standard library has no bounded suffix writer.
type tailBuffer struct {
	capacity int
	buf      []byte
	start    int
	size     int
}

func newTailBuffer(capacity int) *tailBuffer {
	return &tailBuffer{capacity: capacity}
}

// Write keeps the suffix of the stream within capacity. It never fails.
func (t *tailBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if t.capacity <= 0 {
		t.buf = nil
		t.start = 0
		t.size = 0
		return written, nil
	}
	if len(p) == 0 {
		return 0, nil
	}
	if len(t.buf) != t.capacity {
		t.buf = make([]byte, t.capacity)
	}
	if len(p) >= t.capacity {
		copy(t.buf, p[len(p)-t.capacity:])
		t.start = 0
		t.size = t.capacity
		return written, nil
	}

	fill := min(len(p), t.capacity-t.size)
	end := (t.start + t.size) % t.capacity
	first := copy(t.buf[end:], p[:fill])
	copy(t.buf, p[first:fill])
	t.size += fill
	p = p[fill:]

	if len(p) > 0 {
		first = copy(t.buf[t.start:], p)
		copy(t.buf, p[first:])
		t.start = (t.start + len(p)) % t.capacity
	}
	return written, nil
}

// Bytes returns the retained tail in stream order. The returned slice may be
// newly allocated to linearize wrapped content.
func (t *tailBuffer) Bytes() []byte {
	if t.size == 0 {
		return nil
	}
	linear := make([]byte, t.size)
	first := copy(linear, t.buf[t.start:])
	copy(linear[first:], t.buf[:t.size-first])
	return linear
}
