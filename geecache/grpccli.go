package geecache

import (
	"context"
	pb "geecache/pb"
	"log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type GRPCClient struct {
	addr    string
	conn    *grpc.ClientConn
	remover PeerRemover
}

func NewGRPCClient(addr string) *GRPCClient {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to dial: %v", err)
	}
	return &GRPCClient{
		addr: addr,
		conn: conn,
	}
}

func (c *GRPCClient) Get(group string, key string) ([]byte, error) {
	req := &pb.Request{
		Group: group,
		Key:   key,
	}

	client := pb.NewCacheServiceClient(c.conn)
	rsp, err := client.Get(context.Background(), req)
	if err != nil {
		if c.remover != nil {
			c.remover.RemovePeer(c.addr)
		}
		return nil, err
	}

	return rsp.Value, nil
}

var _ PeerGetter = (*GRPCClient)(nil)
