package server

import (
	"bytes"
	"context"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
)

type LimitedServer struct {
	notifyInterval time.Duration
	backend        Backend
	scheme         string
}

func (l *LimitedServer) Range(ctx context.Context, r *etcdserverpb.RangeRequest) (*RangeResponse, error) {
	if len(r.RangeEnd) == 0 {
		return l.get(ctx, r)
	}
	return l.list(ctx, r)
}

func txnHeader(rev int64) *etcdserverpb.ResponseHeader {
	return &etcdserverpb.ResponseHeader{
		Revision: rev,
	}
}

func (l *LimitedServer) Put(ctx context.Context, r *etcdserverpb.PutRequest) (*etcdserverpb.PutResponse, error) {
	rev, kv, err := l.backend.Get(ctx, string(r.Key), "", 1, 0)
	if err != nil {
		return nil, err
	}

	// r.IgnoreValue - update using current value
	value := r.Value
	if r.IgnoreValue {
		value = kv.Value
	}

	// r.IgnoreLease - update using current lease
	lease := r.Lease
	if r.IgnoreLease {
		value = make([]byte, kv.Lease)
	}

	// r.PrevKv - return previous kv before changing
	if kv != nil {
		rev, kv, _, err = l.backend.Update(ctx, string(r.Key), value, rev, lease)
	} else {
		rev, err = l.backend.Create(ctx, string(r.Key), value, lease)
		// PrevKv is empty
	}

	return &etcdserverpb.PutResponse{
		Header: txnHeader(rev),
		PrevKv: toKV(kv),
	}, nil
}

func (l *LimitedServer) DeleteRange(ctx context.Context, r *etcdserverpb.DeleteRangeRequest) (*etcdserverpb.DeleteRangeResponse, error) {
	var key, end string

	key = string(r.Key)
	end = ""
	if len(r.RangeEnd) != 0 {
		// potentially subtly broken here
		key = string(append(r.RangeEnd[:len(r.RangeEnd)-1], r.RangeEnd[len(r.RangeEnd)-1]-1))
		if !strings.HasSuffix(key, "/") {
			key = key + "/"
		}
		end = string(bytes.TrimRight(r.Key, "\x00"))
	}

	rev, kvs, err := l.backend.List(ctx, key, end, 0, 0)
	if err != nil {
		return nil, err
	}

	// this should be rewritten to a single upsert for operation atomicity
	// we could use transactions, but mysql's idea of a transaction is an unfunny joke
	prevKVs := make([]*mvccpb.KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		_, prevKV, ok, _ := l.backend.Delete(ctx, kv.Key, rev)
		if ok {
			prevKVs = append(prevKVs, toKV(prevKV))
		}
	}

	return &etcdserverpb.DeleteRangeResponse{
		Header:  txnHeader(rev),
		Deleted: int64(len(prevKVs)),
		PrevKvs: prevKVs,
	}, nil
}

func (l *LimitedServer) Txn(ctx context.Context, txn *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
	if put := isCreate(txn); put != nil {
		return l.create(ctx, put)
	}
	if rev, key, ok := isDelete(txn); ok {
		return l.delete(ctx, key, rev)
	}
	if rev, key, value, lease, ok := isUpdate(txn); ok {
		return l.update(ctx, rev, key, value, lease)
	}
	if ver, value, ok := isCompact(txn); ok {
		return l.compact(ctx, ver, value)
	}
	return nil, ErrNotSupported
}

type ResponseHeader struct {
	Revision int64
}

type RangeResponse struct {
	Header *etcdserverpb.ResponseHeader
	Kvs    []*KeyValue
	More   bool
	Count  int64
}
