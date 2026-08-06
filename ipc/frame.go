package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// tag identifies which of the three envelope types a frame carries, so a
// single connection can multiplex requests, responses, and events without
// double-unmarshaling to figure out which one it got.
type tag byte

const (
	tagRequest  tag = 'Q'
	tagResponse tag = 'R'
	tagEvent    tag = 'E'
)

// maxFrameSize guards against a corrupt or malicious length header causing an
// unbounded allocation.
const maxFrameSize = 64 << 20

// writeFrame writes tag t followed by a 4-byte big-endian length prefix and
// v's JSON encoding.
func writeFrame(w io.Writer, t tag, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ipc: marshaling frame: %w", err)
	}
	if len(body) > maxFrameSize {
		return fmt.Errorf("ipc: frame of %d bytes exceeds max %d", len(body), maxFrameSize)
	}

	header := make([]byte, 5)
	header[0] = byte(t)
	binary.BigEndian.PutUint32(header[1:], uint32(len(body)))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("ipc: writing frame header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("ipc: writing frame body: %w", err)
	}
	return nil
}

// readFrame reads one frame written by writeFrame, returning its tag and raw
// JSON body for the caller to unmarshal based on that tag.
func readFrame(r io.Reader) (tag, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	t := tag(header[0])
	n := binary.BigEndian.Uint32(header[1:])
	if n > maxFrameSize {
		return 0, nil, fmt.Errorf("ipc: frame of %d bytes exceeds max %d", n, maxFrameSize)
	}

	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, fmt.Errorf("ipc: reading frame body: %w", err)
	}
	return t, body, nil
}

func writeRequest(w io.Writer, req Request) error    { return writeFrame(w, tagRequest, req) }
func writeResponse(w io.Writer, resp Response) error { return writeFrame(w, tagResponse, resp) }
func writeEvent(w io.Writer, ev Event) error         { return writeFrame(w, tagEvent, ev) }
