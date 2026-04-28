# GeeCache

一个轻量级的分布式缓存系统，参考 groupcache 思路：一致性哈希分片、单飞（`singleflight`）、可选 etcd 发现、HTTP 与 gRPC 同端口（cmux）。

## 运行

```bash
go run . -port=9001 -use-etcd=false -peers=http://localhost:9001,http://localhost:9002,http://localhost:9003
```

Windows 多节点示例：`scripts/start-three.ps1`（明文 + etcd）；**节点 mTLS 本机演示**：`scripts/gencerts-mtls.ps1` 与 `scripts/start-three-mtls.ps1`（静态 https peers，无 etcd）。

## HTTP 端点

**默认（仅 `-port`）**：`/metrics`、`/healthz`、`/debug/pprof` 与 **`/geecache/...`** 在同一明文端口；不经 `/geecache` 的路径**不**校验 `X-Geecache-Peer-Token`。

**启用节点 mTLS（`-peer-mtls-listen`）时**：`-port` 上**只有**运维路径（`/healthz`、`/metrics`、`pprof`），**没有** `/geecache`。节点间缓存与 gRPC 仅在 **`-peer-mtls-listen`** 对应的 **TLS** 端口上；环上地址须为 **`https://...`**，且与 **`-peer-base-url`**（本机在环中的身份）一致。

| 路径 | 说明 |
|------|------|
| `GET /healthz` | 存活探测，返回 `200` 与正文 `ok\n`；供 K8s `livenessProbe` / 负载均衡使用。**不要求** `X-Geecache-Peer-Token`。 |
| `GET /metrics` | Prometheus 指标。 |
| `GET/PUT/DELETE /geecache/<group>/<key>` | 缓存 API（默认在 `-port`；mTLS 模式下仅在 peer TLS 端口）；若配置了 `-peer-auth-token`，须带头 `X-Geecache-Peer-Token`（与节点间 gRPC metadata `x-geecache-peer-token` 一致）。 |
| `/debug/pprof/*` | 标准 pprof，勿对公网暴露。 |

## 节点间 mTLS（可选）

用于「节点 ↔ 节点」的 HTTP 与 gRPC（**不**加密面向运维的明文端口）。

1. 准备 CA、服务端证书（SAN 含 peer 主机名）、各节点客户端证书（由 **`-peer-mtls-client-ca`** 所给 CA 签发）。
2. 每个进程指定：
   - **`-peer-mtls-listen`**：例如 `:8443`（与 `-port` 不同）。
   - **`-peer-base-url`**：本节点在一致性哈希中的 URL，例如 `https://node-a.example:8443`，须写入 **`-peers`** 或 etcd 注册值。
   - **`-peer-mtls-cert` / `-peer-mtls-key`**：本机对外 TLS 证书与私钥。
   - **`-peer-mtls-client-ca`**：校验**对端**连接时出示的客户端证书。
   - **`-peer-mtls-server-ca`**：校验**对端**服务端证书。
   - **`-peer-mtls-client-cert` / `-peer-mtls-client-key`**：本机作为客户端访问其它节点时使用。
3. **`-peers`**（静态）或 etcd 中的值一律为 **`https://...`**，且与 mTLS 监听端口一致。

gRPC 出站使用 `grpc` 的 `credentials.NewTLS`；入站在 `tls.NewListener` 之后与原先 h2c 分支相同，**不再**在 `grpc.Server` 上叠一层 `NewTLS`（见 `PITFALLS.md`）。

### 本机演示（Windows）

1. 安装 OpenSSL（如 Git for Windows 自带 `openssl` 在 PATH 中）。
2. 仓库根目录执行：`.\scripts\gencerts-mtls.ps1`（生成 `certs/mtls-demo/`，已加入 `.gitignore`）。
3. 执行：`.\scripts\start-three-mtls.ps1`（会开三个 PowerShell 窗口：`use-etcd=false`，明文 `9001–9003`，节点 HTTPS `9441–9443`）。
4. 探活（明文）：`curl http://localhost:9001/healthz`
5. 读缓存（须走 mTLS 口并带上**客户端证书**）：  
   `curl -k https://127.0.0.1:9441/geecache/scores/Tom --cert certs/mtls-demo/client-cert.pem --key certs/mtls-demo/client-key.pem --cacert certs/mtls-demo/ca.pem`  
   （未带证书时 TLS 握手阶段即失败，属预期。）

沿用 etcd 时，注册到 etcd 的节点地址须为 `https://...` 且与 `-peer-mtls-listen` 一致；此时可仿照 `scripts/start-three-mtls.ps1` 自行拼参数，不必改原 `start-three.ps1`（原脚本仍为仅明文 + etcd）。

## 常用参数

- **`-use-etcd`**：为 `true` 时用 etcd 注册与发现；`RegisterWithEtcd` 在 **HTTP/gRPC 已开始接受连接之后** 执行，避免「etcd 可见但端口未就绪」。
- **`-peers`**：仅当 `-use-etcd=false` 时生效，逗号分隔，须包含本机 URL；启用 peer 客户端 TLS 时须为 `https://`。
- **`-peer-auth-token`**：非空时启用节点间共享密钥（HTTP 头 + gRPC metadata）；须在首次拉齐 peer 列表前通过进程内 `SetPeerAuthToken` 配置（`main` 已处理）。
- **`-persist-path` / `-warm` / `-warm-keys`**：Bolt 持久化与启动预热。

更多踩坑说明见 **`PITFALLS.md`**；演进规划见 **`ROADMAP.md`**。

## 许可证

MIT License
