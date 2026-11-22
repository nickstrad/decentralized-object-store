package node

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	MaxFrame       = 16 << 20
	MaxDatagram    = 65507
	frameLenSize   = 4
	frameVersion   = 1
	kindRequest    = 1
	kindResponse   = 2
	requestHeader  = 1 + 1 + 8 + 1
	responseHeader = 1 + 1 + 8 + 1 + 2
	datagramHeader = 1 + 1
)

type requestFrame struct {
	ID   uint64
	From env.NodeID
	Body []byte
}

type responseFrame struct {
	ID   uint64
	Code env.Code
	Msg  string
	Body []byte
}

func appendFrame(dst, payload []byte) ([]byte, error) {
	if len(payload) > MaxFrame {
		return dst, fmt.Errorf("node: frame of %d bytes over %d: %w", len(payload), MaxFrame, env.ErrTooLarge)
	}
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
	return append(dst, payload...), nil
}

func readFrame(r io.Reader, limit int) ([]byte, error) {
	var hdr [frameLenSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("node: frame header: %w: %w", err, ErrFrame)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if uint64(n) > uint64(limit) {
		return nil, fmt.Errorf("node: frame of %d bytes over %d: %w", n, limit, env.ErrTooLarge)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("node: frame body: %w: %w", err, ErrFrame)
	}
	return buf, nil
}

func encodeRequest(f requestFrame) ([]byte, error) {
	if len(f.From) == 0 || len(f.From) > 255 {
		return nil, fmt.Errorf("node: request from %q: %w", f.From, env.ErrInvalidArgument)
	}
	b := make([]byte, 0, requestHeader+len(f.From)+len(f.Body))
	b = append(b, frameVersion, kindRequest)
	b = binary.BigEndian.AppendUint64(b, f.ID)
	b = append(b, byte(len(f.From)))
	b = append(b, f.From...)
	return append(b, f.Body...), nil
}

func decodeRequest(b []byte) (requestFrame, error) {
	if len(b) < requestHeader || b[0] != frameVersion || b[1] != kindRequest {
		return requestFrame{}, fmt.Errorf("node: request frame: %w", ErrFrame)
	}
	id := binary.BigEndian.Uint64(b[2:10])
	n := int(b[10])
	rest := b[11:]
	if n == 0 || len(rest) < n {
		return requestFrame{}, fmt.Errorf("node: request frame sender: %w", ErrFrame)
	}
	return requestFrame{ID: id, From: env.NodeID(rest[:n]), Body: rest[n:]}, nil
}

func encodeResponse(f responseFrame) []byte {
	msg := f.Msg
	if len(msg) > env.MaxErrorMessage {
		msg = msg[:env.MaxErrorMessage]
	}
	b := make([]byte, 0, responseHeader+len(msg)+len(f.Body))
	b = append(b, frameVersion, kindResponse)
	b = binary.BigEndian.AppendUint64(b, f.ID)
	b = append(b, byte(f.Code))
	b = binary.BigEndian.AppendUint16(b, uint16(len(msg)))
	b = append(b, msg...)
	return append(b, f.Body...)
}

func decodeResponse(b []byte) (responseFrame, error) {
	if len(b) < responseHeader || b[0] != frameVersion || b[1] != kindResponse {
		return responseFrame{}, fmt.Errorf("node: response frame: %w", ErrFrame)
	}
	f := responseFrame{ID: binary.BigEndian.Uint64(b[2:10]), Code: env.Code(b[10])}
	n := int(binary.BigEndian.Uint16(b[11:13]))
	rest := b[13:]
	if n > env.MaxErrorMessage || len(rest) < n {
		return responseFrame{}, fmt.Errorf("node: response frame message: %w", ErrFrame)
	}
	f.Msg = string(rest[:n])
	f.Body = rest[n:]
	if f.Code != env.CodeOK && len(f.Body) != 0 {
		return responseFrame{}, fmt.Errorf("node: response frame: error with body: %w", ErrFrame)
	}
	return f, nil
}

func encodeDatagram(from env.NodeID, body []byte) ([]byte, error) {
	if len(from) == 0 || len(from) > 255 {
		return nil, fmt.Errorf("node: datagram from %q: %w", from, env.ErrInvalidArgument)
	}
	if datagramHeader+len(from)+len(body) > MaxDatagram {
		return nil, fmt.Errorf("node: datagram of %d bytes: %w", len(body), env.ErrTooLarge)
	}
	b := make([]byte, 0, datagramHeader+len(from)+len(body))
	b = append(b, frameVersion, byte(len(from)))
	b = append(b, from...)
	return append(b, body...), nil
}

func decodeDatagram(b []byte) (env.NodeID, []byte, error) {
	if len(b) < datagramHeader || b[0] != frameVersion {
		return "", nil, fmt.Errorf("node: datagram: %w", ErrFrame)
	}
	n := int(b[1])
	if n == 0 || len(b)-datagramHeader < n {
		return "", nil, fmt.Errorf("node: datagram sender: %w", ErrFrame)
	}
	return env.NodeID(b[2 : 2+n]), b[2+n:], nil
}
