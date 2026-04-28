package geecache

import (
	"context"
	"errors"
	pb "geecache/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type GRPCServer struct {
	pb.UnimplementedCacheServiceServer
}

// NewGRPCServer 创建 gRPC 服务。transportCreds 非 nil（例如 credentials.NewTLS）时启用对应传输层凭据；
// 为 nil 时接受明文 h2c，与 cmux 下未再套 TLS 的 gRPC 分支一致。
func NewGRPCServer(peerAuthToken string, transportCreds credentials.TransportCredentials) *grpc.Server {
	opts := []grpc.ServerOption{}
	if transportCreds != nil {
		opts = append(opts, grpc.Creds(transportCreds))
	}
	if peerAuthToken != "" {
		opts = append(opts, grpc.ChainUnaryInterceptor(unaryPeerAuthInterceptor(peerAuthToken)))
	}
	grpcServer := grpc.NewServer(opts...)
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
