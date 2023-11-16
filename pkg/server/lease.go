package server

import (
	"context"
	"fmt"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
)

// explicit interface check
var _ etcdserverpb.LeaseServer = (*KVServerBridge)(nil)

// this interface will require an additional table f-keying into events
func (s *KVServerBridge) LeaseGrant(ctx context.Context, req *etcdserverpb.LeaseGrantRequest) (*etcdserverpb.LeaseGrantResponse, error) {
	return &etcdserverpb.LeaseGrantResponse{
		Header: &etcdserverpb.ResponseHeader{},
		ID:     req.TTL,
		TTL:    req.TTL,
	}, nil
}

func (s *KVServerBridge) LeaseRevoke(ctx context.Context, req *etcdserverpb.LeaseRevokeRequest) (*etcdserverpb.LeaseRevokeResponse, error) {
	// req.ID
	return &etcdserverpb.LeaseRevokeResponse{
		Header: &etcdserverpb.ResponseHeader{},
	}, nil
}

func (s *KVServerBridge) LeaseKeepAlive(srv etcdserverpb.Lease_LeaseKeepAliveServer) error {
	// implement later
	return fmt.Errorf("lease keep alive is not supported")
}

func (s *KVServerBridge) LeaseTimeToLive(ctx context.Context, req *etcdserverpb.LeaseTimeToLiveRequest) (*etcdserverpb.LeaseTimeToLiveResponse, error) {
	keys := make([][]byte, 0)
	/*
	if req.Keys {
		keys = append(keys, ??)
	}
	*/
	return &etcdserverpb.LeaseTimeToLiveResponse{
		Header: &etcdserverpb.ResponseHeader{},
		ID: req.ID,
		TTL: 86500,  // currentTime-grantTime>0
		GrantedTTL: 86500,  // req.TTL
		Keys: keys,
	}, nil
}

func (s *KVServerBridge) LeaseLeases(ctx context.Context, req *etcdserverpb.LeaseLeasesRequest) (*etcdserverpb.LeaseLeasesResponse, error) {
	leases := make([]*etcdserverpb.LeaseStatus, 0)
	return &etcdserverpb.LeaseLeasesResponse{
		Header: &etcdserverpb.ResponseHeader{},
		Leases: leases,
	}, nil
}
