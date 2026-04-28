package geecache

import (
	"context"
	"crypto/subtle"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// PeerAuthHTTPHeader 节点间 HTTP 调用（GET/PUT/DELETE /geecache/...）时携带的共享密钥头名。
// 与终端用户 curl 区分：对外网关可不要求此头，仅集群内节点配置相同 -peer-auth-token。
const PeerAuthHTTPHeader = "X-Geecache-Peer-Token"

const peerAuthMetadataKey = "x-geecache-peer-token"

func peerAppendOutgoingContext(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, peerAuthMetadataKey, token)
}

func unaryPeerAuthInterceptor(expected string) grpc.UnaryServerInterceptor {
	if expected == "" {
		return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
			return handler(ctx, req)
		}
	}
	exp := []byte(expected)
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "geecache: peer auth: missing metadata")
		}
		vals := md.Get(peerAuthMetadataKey)
		if len(vals) != 1 || subtle.ConstantTimeCompare([]byte(vals[0]), exp) != 1 {
			return nil, status.Error(codes.Unauthenticated, "geecache: peer auth: invalid token")
		}
		return handler(ctx, req)
	}
}

func peerHTTPAuthOK(expected, got string) bool {
	if expected == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
