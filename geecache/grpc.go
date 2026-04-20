package geecache

import (
	"context"
	"fmt"
	pb "geecache/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type GRPCServer struct {
	pb.UnimplementedCacheServiceServer
	groups *map[string]*Group
}

func NewGRPCServer(groups *map[string]*Group) *grpc.Server {
	s := &GRPCServer{
		groups: groups,
	}

	grpcServer := grpc.NewServer()
	pb.RegisterCacheServiceServer(grpcServer, s)
	reflection.Register(grpcServer)

	return grpcServer
}

func (s *GRPCServer) Get(ctx context.Context, in *pb.Request) (*pb.Response, error) {
	group := GetGroup(in.Group)
	if group == nil {
		return nil, fmt.Errorf("no such group: %s", in.Group)
	}

	view, err := group.Get(in.Key)
	if err != nil {
		return nil, err
	}

	return &pb.Response{
		Value: view.ByteSlice(),
	}, nil
}
