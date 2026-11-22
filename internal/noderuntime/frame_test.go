package noderuntime

import (
	"bytes"
	"errors"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func TestFrameRoundTrips(t *testing.T) {
	req := requestFrame{ID: 7, From: "a:1", Body: []byte("body")}
	p, err := encodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	f, err := appendFrame(nil, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(bytes.NewReader(f), MaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeRequest(got)
	if err != nil || back.ID != 7 || back.From != "a:1" || string(back.Body) != "body" {
		t.Fatalf("request round trip: %+v %v", back, err)
	}
	resp := responseFrame{ID: 9, Code: env.CodeOK, Body: []byte{1, 2}}
	r, err := decodeResponse(encodeResponse(resp))
	if err != nil || r.ID != 9 || r.Code != env.CodeOK || !bytes.Equal(r.Body, []byte{1, 2}) {
		t.Fatalf("response round trip: %+v %v", r, err)
	}
	long := responseFrame{ID: 1, Code: 5, Msg: string(make([]byte, 500))}
	if r, err := decodeResponse(encodeResponse(long)); err != nil || len(r.Msg) != env.MaxErrorMessage {
		t.Fatalf("long message: %d %v", len(r.Msg), err)
	}
	d, err := encodeDatagram("x:1", []byte("hi"))
	if err != nil {
		t.Fatal(err)
	}
	from, body, err := decodeDatagram(d)
	if err != nil || from != "x:1" || string(body) != "hi" {
		t.Fatalf("datagram round trip: %q %q %v", from, body, err)
	}
}

func TestFrameDecodersRejectMalformedInput(t *testing.T) {
	good, _ := encodeRequest(requestFrame{ID: 1, From: "a", Body: []byte("b")})
	rows := []struct {
		name string
		run  func() error
		want error
	}{
		{"an empty request", func() error { _, err := decodeRequest(nil); return err }, node.ErrFrame},
		{"a request with a wrong version", func() error { b := bytes.Clone(good); b[0] = 9; _, err := decodeRequest(b); return err }, node.ErrFrame},
		{"a request with a response kind", func() error { b := bytes.Clone(good); b[1] = kindResponse; _, err := decodeRequest(b); return err }, node.ErrFrame},
		{"a request whose sender runs past the end", func() error { b := bytes.Clone(good[:11]); b[10] = 40; _, err := decodeRequest(b); return err }, node.ErrFrame},
		{"a request with an empty sender", func() error { b := bytes.Clone(good); b[10] = 0; _, err := decodeRequest(b); return err }, node.ErrFrame},
		{"a short response", func() error { _, err := decodeResponse([]byte{1, 2, 3}); return err }, node.ErrFrame},
		{"a response whose message runs past the end", func() error {
			b := encodeResponse(responseFrame{ID: 1, Code: 3, Msg: "abc"})
			b[12] = 200
			_, err := decodeResponse(b)
			return err
		}, node.ErrFrame},
		{"an error response carrying a body", func() error {
			_, err := decodeResponse(append(encodeResponse(responseFrame{ID: 1, Code: 3}), 1))
			return err
		}, node.ErrFrame},
		{"a datagram with a wrong version", func() error { _, _, err := decodeDatagram([]byte{2, 1, 'a'}); return err }, node.ErrFrame},
		{"a datagram whose sender runs past the end", func() error { _, _, err := decodeDatagram([]byte{1, 9, 'a'}); return err }, node.ErrFrame},
		{"a truncated frame header", func() error { _, err := readFrame(bytes.NewReader([]byte{0, 0}), MaxFrame); return err }, node.ErrFrame},
		{"a truncated frame body", func() error { _, err := readFrame(bytes.NewReader([]byte{0, 0, 0, 5, 1}), MaxFrame); return err }, node.ErrFrame},
		{"a frame over the limit", func() error { _, err := readFrame(bytes.NewReader([]byte{0, 0, 0, 9}), 8); return err }, env.ErrTooLarge},
		{"a payload over the frame limit", func() error { _, err := appendFrame(nil, make([]byte, MaxFrame+1)); return err }, env.ErrTooLarge},
		{"a datagram over the size limit", func() error { _, err := encodeDatagram("a", make([]byte, MaxDatagram)); return err }, env.ErrTooLarge},
		{"a request from no one", func() error { _, err := encodeRequest(requestFrame{}); return err }, env.ErrInvalidArgument},
		{"a datagram from no one", func() error { _, err := encodeDatagram("", nil); return err }, env.ErrInvalidArgument},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if err := r.run(); !errors.Is(err, r.want) {
				t.Fatalf("got %v, want %v", err, r.want)
			}
		})
	}
}

func TestFrameLimitCoversMerkleRepliesAndShardRequests(t *testing.T) {
	if merkle.MaxReplyBytes+responseHeader+env.MaxErrorMessage > MaxFrame {
		t.Fatalf("a full merkle reply (%d bytes) does not fit the %d-byte frame", merkle.MaxReplyBytes, MaxFrame)
	}
	l := storage.DefaultLayout()
	blob := make([]byte, storage.HeaderSize+l.StripeSize/l.K)
	id := storage.ShardID{Bucket: string(bytes.Repeat([]byte("b"), storage.MaxBucketLen)), Version: 1, Stripe: 1 << 31, Index: 255}
	req := storage.EncodeShardRequest(storage.OpPutShard, id, blob)
	if got := len(req) + 1 + requestHeader + 255; got > node.ShardRequestSize(l)+requestHeader+255 || got > MaxFrame {
		t.Fatalf("a default shard request of %d bytes exceeds its bound %d or the frame %d", got, node.ShardRequestSize(l), MaxFrame)
	}
	big := storage.Layout{K: 1, M: 0, StripeSize: storage.MaxStripeSize}
	if err := (node.Config{Layout: big}).Validate(); !errors.Is(err, node.ErrConfig) {
		t.Fatalf("a layout whose shard request exceeds the frame was accepted: %v", err)
	}
}

func FuzzFrame(f *testing.F) {
	good, _ := encodeRequest(requestFrame{ID: 3, From: "n:1", Body: []byte("x")})
	framed, _ := appendFrame(nil, good)
	f.Add(framed)
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 1})
	f.Add(append([]byte{0, 0, 0, 13}, encodeResponse(responseFrame{ID: 1, Code: 0})...))
	f.Add([]byte{1, 3, 'a', 'b', 'c', 9})
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, err := readFrame(bytes.NewReader(b), 1<<16); err == nil {
			if len(p) > 1<<16 || len(b) < frameLenSize+len(p) {
				t.Fatalf("frame of %d bytes from %d input bytes", len(p), len(b))
			}
			if r, err := decodeRequest(p); err == nil {
				again, err := encodeRequest(r)
				if err != nil || !bytes.Equal(again, p) {
					t.Fatalf("request does not re-encode to its bytes: %v", err)
				}
			}
			if r, err := decodeResponse(p); err == nil && len(r.Msg) <= env.MaxErrorMessage {
				if again := encodeResponse(r); !bytes.Equal(again, p) {
					t.Fatalf("response does not re-encode to its bytes")
				}
			}
		} else if !errors.Is(err, node.ErrFrame) && !errors.Is(err, env.ErrTooLarge) {
			t.Fatalf("frame error %v is neither node.ErrFrame nor ErrTooLarge", err)
		}
		if from, body, err := decodeDatagram(b); err == nil {
			again, err := encodeDatagram(from, body)
			if err == nil && !bytes.Equal(again, b) {
				t.Fatalf("datagram does not re-encode to its bytes")
			}
		}
	})
}
