# 1. gRPC 与 cmux 同端口：SETTINGS 丢包导致一直连不上

同一端口用 [cmux](https://github.com/soheilhy/cmux) 分流 HTTP 与 gRPC 时，若用 **`m.Match(cmux.HTTP2HeaderField("content-type", "application/grpc"))`**，cmux 在嗅探阶段会把 HTTP/2 Framer 的写端接到 **`ioutil.Discard`**，**对客户端的 SETTINGS 应答不会真正写回 TCP**。而 **grpc-go**（明文 h2c）会等对端 SETTINGS 协商完成后再发带 `content-type: application/grpc` 的 HEADERS，于是出现：**客户端卡在 “waiting for connections to become ready” / `DeadlineExceeded`，服务端 matcher 也等不到 HEADERS**。节点间 `GRPCClient` 与本地 `grpcping` 表现一致。

**做法**：对 gRPC 这一路改用 **`m.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))`**，让 SETTINGS 写回真实连接（见 `main.go` 中 `grpcL` 的注册方式）。cmux 文档亦说明：若客户端会阻塞等待 SETTINGS，不要用仅 `HTTP2HeaderField` 的 `Match` 写法。
