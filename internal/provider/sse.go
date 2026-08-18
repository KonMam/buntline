package provider

import (
	"bufio"
	"bytes"
	"io"
)

// sseScanner reads server-sent events from an OpenAI-compatible stream.
// These streams only ever use `data:` fields, one JSON payload per event,
// separated by blank lines. Multi-line data fields are joined with \n per
// the SSE spec; CR is tolerated at line ends.
type sseScanner struct {
	s *bufio.Scanner
}

func newSSEScanner(r io.Reader) *sseScanner {
	s := bufio.NewScanner(r)
	// Tool-call argument chunks can be large; default 64KB line cap is not
	// safely above what local servers emit for long single-line payloads.
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &sseScanner{s: s}
}

// Next returns the data payload of the next event, or io.EOF when the
// stream ends. Comment lines (":") and non-data fields are skipped.
func (sc *sseScanner) Next() ([]byte, error) {
	var data [][]byte
	for sc.s.Scan() {
		line := bytes.TrimSuffix(sc.s.Bytes(), []byte("\r"))
		if len(line) == 0 {
			if len(data) > 0 {
				return bytes.Join(data, []byte("\n")), nil
			}
			continue // blank line before any data: keep-alive, skip
		}
		if bytes.HasPrefix(line, []byte(":")) {
			continue // comment / keep-alive
		}
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			rest = bytes.TrimPrefix(rest, []byte(" "))
			// Scanner reuses its buffer; copy before holding across Scan.
			data = append(data, bytes.Clone(rest))
		}
		// Other fields (event:, id:, retry:) don't occur on
		// OpenAI-compatible streams; ignore them if they do.
	}
	if len(data) > 0 {
		return bytes.Join(data, []byte("\n")), nil
	}
	if err := sc.s.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}
