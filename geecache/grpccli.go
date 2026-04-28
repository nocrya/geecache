package geecache

import (
	"context"
	"fmt"
	pb "geecache/pb"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultGRPCTimeout = 3 * time.Second

type GRPCClient struct {
	peer      string
	target    string
	conn      *grpc.ClientConn
	stub      pb.CacheServiceClient
	peerToken string
}

// NewGRPCClient 拨号 target（一般为 host:port）。transportCreds 为 nil 时使用 insecure（h2c）；否则使用 TLS 等凭据。
func NewGRPCClient(peer string, target string, peerToken string, transportCreds credentials.TransportCredentials) (*GRPCClient, error) {
	tc := transportCreds
	if tc == nil {
		tc = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(tc))
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %s: %w", target, err)
	}
	return &GRPCClient{
		peer:      peer,
		target:    target,
		conn:      conn,
		stub:      pb.NewCacheServiceClient(conn),
		peerToken: peerToken,
	}, nil
}

// Close 关闭 gRPC 连接（调试用 grpcping 等可在退出前调用）。
func (c *GRPCClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *GRPCClient) Get(group string, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGRPCTimeout)
	defer cancel()
	ctx = peerAppendOutgoingContext(ctx, c.peerToken)
	rsp, err := c.stub.Get(ctx, &pb.Request{
		Group: group,
		Key:   key,
	})

	if err != nil {
		return nil, err
	}
	return rsp.Value, nil
}

// Set 在远端节点执行写入（归属校验在服务端）。
func (c *GRPCClient) Set(group, key string, value []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGRPCTimeout)
	defer cancel()
	ctx = peerAppendOutgoingContext(ctx, c.peerToken)
	_, err := c.stub.Set(ctx, &pb.SetRequest{Group: group, Key: key, Value: value})
	if err != nil {
		return err
	}
	return nil
}

// Invalidate 仅失效远端本机条目。
func (c *GRPCClient) Invalidate(group, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGRPCTimeout)
	defer cancel()
	ctx = peerAppendOutgoingContext(ctx, c.peerToken)
	_, err := c.stub.Invalidate(ctx, &pb.InvalidateRequest{Group: group, Key: key})
	if err != nil {
		return err
	}
	return nil
}

// Purge 在远端归属节点执行 PurgeKey。
func (c *GRPCClient) Purge(group, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGRPCTimeout)
	defer cancel()
	ctx = peerAppendOutgoingContext(ctx, c.peerToken)
	_, err := c.stub.Purge(ctx, &pb.InvalidateRequest{Group: group, Key: key})
	if err != nil {
		return err
	}
	return nil
}

func (c *GRPCClient) Peer() string  { return c.peer }
func (c *GRPCClient) Proto() string { return "grpc" }

var _ PeerGetter = (*GRPCClient)(nil)
