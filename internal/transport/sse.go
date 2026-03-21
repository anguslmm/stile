package transport

import (
	"bufio"
	"io"
	"strings"
)

// trimOneLeadingSpace removes a single leading space per the SSE spec:
// "If value starts with a U+0020 SPACE character, remove it from value."
func trimOneLeadingSpace(s string) string {
	if strings.HasPrefix(s, " ") {
		return s[1:]
	}
	return s
}

// SSEEvent represents a single Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
}

// SSEReader reads SSE events from an io.Reader.
type SSEReader struct {
	scanner *bufio.Scanner
}

// NewSSEReader creates an SSEReader wrapping the given reader.
func NewSSEReader(r io.Reader) *SSEReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1 MB max line
	return &SSEReader{scanner: s}
}

// Next returns the next SSE event from the stream.
// Returns io.EOF when the stream is exhausted.
func (r *SSEReader) Next() (*SSEEvent, error) {
	var event SSEEvent
	hasFields := false

	for r.scanner.Scan() {
		line := r.scanner.Text()

		// Blank line signals end of an event.
		if line == "" {
			if hasFields {
				return &event, nil
			}
			continue
		}

		if after, ok := strings.CutPrefix(line, "event:"); ok {
			event.Event = trimOneLeadingSpace(after)
			hasFields = true
		} else if after, ok := strings.CutPrefix(line, "data:"); ok {
			if event.Data != "" {
				event.Data += "\n"
			}
			event.Data += trimOneLeadingSpace(after)
			hasFields = true
		}
		// Ignore id:, retry:, and comment lines (starting with ':').
	}

	if err := r.scanner.Err(); err != nil {
		return nil, err
	}

	// If we collected fields before EOF (no trailing blank line), return them.
	if hasFields {
		return &event, nil
	}

	return nil, io.EOF
}
