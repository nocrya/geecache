package geecache

import (
	"context"
	"errors"
	pb "geecache/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type GRPCServer struct {
	pb.UnimplementedCacheServiceServer
}

func NewGRPCServer() *grpc.Server {

	grpcServer := grpc.NewServer()
	pb.RegisterCacheServiceServer(grpcServer, &GRPCServer{})
	reflection.Register(grpcServer)

	return grpcServer
}

func (s *GRPCServer) Get(ctx context.Context, in *pb.Request) (*pb.Response, error) {
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	g := GetGroup(in.Group)
	if g == nil {
		return nil, status.Errorf(codes.NotFound, "no such group: %s", in.Group)
	}
	view, err := g.Get(in.Key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &pb.Response{Value: view.ByteSlice()}, nil
}

func (s *GRPCServer) Set(ctx context.Context, in *pb.SetRequest) (*pb.Empty, error) {
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	g := GetGroup(in.Group)
	if g == nil {
		return nil, status.Errorf(codes.NotFound, "no such group: %s", in.Group)
	}
	if err := g.Set(in.Key, in.Value); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &pb.Empty{}, nil
}

func (s *GRPCServer) Invalidate(ctx context.Context, in *pb.InvalidateRequest) (*pb.Empty, error) {
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	g := GetGroup(in.Group)
	if g == nil {
		return nil, status.Errorf(codes.NotFound, "no such group: %s", in.Group)
	}
	if err := g.InvalidateLocal(in.Key); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &pb.Empty{}, nil
}

func (s *GRPCServer) Purge(ctx context.Context, in *pb.InvalidateRequest) (*pb.Empty, error) {
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	g := GetGroup(in.Group)
	if g == nil {
		return nil, status.Errorf(codes.NotFound, "no such group: %s", in.Group)
	}
	if err := g.PurgeKey(in.Key); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &pb.Empty{}, nil
}
