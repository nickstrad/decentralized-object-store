package e2e

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// chAPI is the simulated-network channel byte reserved for client-to-node API
// calls, chosen outside the node's own protocol channel numbers so apiNode can
// intercept client requests and pass every peer channel straight through.
const chAPI byte = 0xA0

const (
	opCreate byte = iota + 1
	opPut
	opGet
	opDelete
	opList
	opPutTag
	opPutACL
	opSetQuota
	opQuota
	opStartMigration
	opMigration
	opTags
	opACL
)

var errBadCall = errors.New("e2e: malformed client call")

type apiArgs struct {
	Spec      storage.BucketSpec `json:",omitempty"`
	Bucket    string             `json:",omitempty"`
	Key       string             `json:",omitempty"`
	Name      string             `json:",omitempty"`
	Value     string             `json:",omitempty"`
	Principal string             `json:",omitempty"`
	Perm      string             `json:",omitempty"`
	To        string             `json:",omitempty"`
	ID        string             `json:",omitempty"`
	Bytes     uint64             `json:",omitempty"`
	Objects   uint64             `json:",omitempty"`
	Cond      storage.Condition
}

type apiResult struct {
	Info  storage.ObjectInfo    `json:",omitempty"`
	Infos []storage.ObjectInfo  `json:",omitempty"`
	Tag   storage.TagInfo       `json:",omitempty"`
	Tags  []storage.TagInfo     `json:",omitempty"`
	ACL   storage.ACLInfo       `json:",omitempty"`
	Quota storage.QuotaInfo     `json:",omitempty"`
	Mig   storage.MigrationInfo `json:",omitempty"`
}

// frame prefixes a JSON header with its uvarint length followed by raw payload
// bytes so object data crosses the simulated transport without base64 bloat.
func frame(head []byte, data []byte) []byte {
	out := make([]byte, 0, len(head)+len(data)+12)
	out = binary.AppendUvarint(out, uint64(len(head)))
	out = append(out, head...)
	return append(out, data...)
}

func unframe(b []byte) ([]byte, []byte, error) {
	n, w := binary.Uvarint(b)
	if w <= 0 || n > uint64(len(b)-w) {
		return nil, nil, errBadCall
	}
	return b[w : w+int(n)], b[w+int(n):], nil
}

func encodeCall(op byte, a apiArgs, data []byte) ([]byte, error) {
	head, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return append([]byte{chAPI, op}, frame(head, data)...), nil
}

func encodeResult(r apiResult, data []byte) ([]byte, error) {
	head, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return frame(head, data), nil
}

func decodeResult(b []byte) (apiResult, []byte, error) {
	var r apiResult
	head, data, err := unframe(b)
	if err != nil {
		return r, nil, err
	}
	if err := json.Unmarshal(head, &r); err != nil {
		return r, nil, fmt.Errorf("%w: %v", errBadCall, err)
	}
	return r, data, nil
}

// apiNode wraps a simulated node process to dispatch chAPI client requests
// against the node's real methods while forwarding all other channels unchanged.
// Errors cross the wire via node.CodeTable so callers can use errors.Is.
type apiNode struct {
	p         env.Process
	n         *node.Node
	onPut     func(n *node.Node, bucket, key string)
	intercept func(n *node.Node, op byte) error
}

func (a *apiNode) Start()                               { a.p.Start() }
func (a *apiNode) OnDatagram(from env.NodeID, m []byte) { a.p.OnDatagram(from, m) }
func (a *apiNode) EncodeState() []byte                  { return a.p.EncodeState() }

func (a *apiNode) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	if len(req) == 0 || req[0] != chAPI {
		a.p.OnRequest(from, req, reply)
		return
	}
	n := a.n
	if n == nil {
		reply(nil, storage.ErrUnavailable)
		return
	}
	if len(req) < 2 {
		reply(nil, errBadCall)
		return
	}
	head, data, err := unframe(req[2:])
	var args apiArgs
	if err == nil {
		err = json.Unmarshal(head, &args)
	}
	if err != nil {
		reply(nil, fmt.Errorf("%w: %v", errBadCall, err))
		return
	}
	send := func(r apiResult, data []byte, err error) {
		if err != nil {
			reply(nil, err)
			return
		}
		b, err := encodeResult(r, data)
		reply(b, err)
	}
	if a.intercept != nil {
		if err := a.intercept(n, req[1]); err != nil {
			reply(nil, err)
			return
		}
	}
	switch req[1] {
	case opCreate:
		send(apiResult{}, nil, n.CreateBucket(args.Spec))
	case opPut:
		if a.onPut != nil {
			a.onPut(n, args.Bucket, args.Key)
		}
		n.PutObject(storage.PutRequest{Bucket: args.Bucket, Key: args.Key, Data: data, Cond: args.Cond}, func(oi storage.ObjectInfo, err error) {
			send(apiResult{Info: oi}, nil, err)
		})
	case opGet:
		n.GetObject(args.Bucket, args.Key, func(oi storage.ObjectInfo, b []byte, err error) {
			send(apiResult{Info: oi}, b, err)
		})
	case opDelete:
		oi, err := n.DeleteObject(args.Bucket, args.Key, args.Cond)
		send(apiResult{Info: oi}, nil, err)
	case opList:
		ois, err := n.ListObjects(args.Bucket, "", 0)
		send(apiResult{Infos: ois}, nil, err)
	case opPutTag:
		ti, err := n.PutTag(args.Bucket, args.Key, args.Name, args.Value, args.Cond)
		send(apiResult{Tag: ti}, nil, err)
	case opPutACL:
		ai, err := n.PutACL(args.Bucket, args.Principal, args.Perm, args.Cond)
		send(apiResult{ACL: ai}, nil, err)
	case opSetQuota:
		q, err := n.SetQuota(args.Bucket, args.Bytes, args.Objects)
		send(apiResult{Quota: q}, nil, err)
	case opQuota:
		q, err := n.Quota(args.Bucket)
		send(apiResult{Quota: q}, nil, err)
	case opStartMigration:
		mi, err := n.StartMigration(args.Bucket, args.To)
		send(apiResult{Mig: mi}, nil, err)
	case opMigration:
		mi, err := n.Migration(args.ID)
		send(apiResult{Mig: mi}, nil, err)
	case opTags:
		ts, err := n.Tags(args.Bucket, args.Key)
		send(apiResult{Tags: ts}, nil, err)
	case opACL:
		ai, err := n.ACL(args.Bucket, args.Principal)
		send(apiResult{ACL: ai}, nil, err)
	default:
		reply(nil, errBadCall)
	}
}

// clientProc is the inert simulated process placed in each region to send chAPI
// requests over the simulated network so client calls experience partitions,
// latency, and lost responses just like peer traffic.
type clientProc struct{ e env.Env }

func (c *clientProc) Start()                        {}
func (c *clientProc) OnDatagram(env.NodeID, []byte) {}
func (c *clientProc) EncodeState() []byte           { return []byte(c.e.ID) }
func (c *clientProc) OnRequest(_ env.NodeID, _ []byte, r env.Reply) {
	r(nil, errBadCall)
}
