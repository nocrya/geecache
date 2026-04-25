package geecache

import (
	"context"
	"fmt"
	pb "geecache/pb"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultGRPCTimeout = 3 * time.Second

type GRPCClient struct {
	peer    string
	target  string
	conn    *grpc.ClientConn
	stub    pb.CacheServiceClient
	remover PeerRemover
}

func NewGRPCClient(peer string, target string, remover PeerRemover) (*GRPCClient, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %s: %w", target, err)
	}
	return &GRPCClient{
		peer:    peer,
		target:  target,
		remover: remover,
		conn:    conn,
		stub:    pb.NewCacheServiceClient(conn),
	}, nil
}

func (c *GRPCClient) Get(group string, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGRPCTimeout)
	defer cancel()
	rsp, err := c.stub.Get(ctx, &pb.Request{
		Group: group,
		Key:   key,
	})

	if err != nil {
		if c.remover != nil {
			c.remover.RemovePeer(c.peer)
		}
		return nil, err
	}
	return rsp.Value, nil
}

// Set 在远端节点执行写入（归属校验在服务端）。
func (c *GRPCClient) Set(group, key string, value []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGRPCTimeout)
	defer cancel()
	_, err := c.stub.Set(ctx, &pb.SetRequest{Group: group, Key: key, Value: value})
	if err != nil {
		if c.remover != nil {
			c.remover.RemovePeer(c.peer)
		}
		return err
	}
	return nil
}

// Invalidate 仅失效远端本机条目。
func (c *GRPCClient) Invalidate(group, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGRPCTimeout)
	defer cancel()
	_, err := c.stub.Invalidate(ctx, &pb.InvalidateRequest{Group: group, Key: key})
	if err != nil {
		if c.remover != nil {
			c.remover.RemovePeer(c.peer)
		}
		return err
	}
	return nil
}

// Purge 在远端归属节点执行 PurgeKey。
func (c *GRPCClient) Purge(group, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGRPCTimeout)
	defer cancel()
	_, err := c.stub.Purge(ctx, &pb.InvalidateRequest{Group: group, Key: key})
	if err != nil {
		if c.remover != nil {
			c.remover.RemovePeer(c.peer)
		}
		return err
	}
	return nil
}

func (c *GRPCClient) Peer() string  { return c.peer }
func (c *GRPCClient) Proto() string { return "grpc" }

var _ PeerGetter = (*GRPCClient)(nil)
