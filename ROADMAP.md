# GeeCache Demo 未来路线图

---

## 当前仓库已具备的能力（基线）

- 分布式读路径：`Group.Get` → 本地 LRU → 一致性哈希选 peer → HTTP 或 gRPC 拉取 → 本地 Getter 回源；`singleflight` 防击穿。
- 同一端口：`cmux` 复用 HTTP（含 `/geecache/...`）与 gRPC。
- 服务发现：etcd 注册、租约、Watch 更新 peer 列表。
- 工程化片段：peer 超时（HTTP / gRPC）、Prometheus 指标（`/metrics`）、`PeerPicker` 抽象、`mainCache` 指针化等。

后续阶段均在此基础上叠加。

---

## 阶段 1：工程健壮性

**目标：** 部署与网络异常下行为可预期，不因单点假死拖垮进程。

| 工作项 | 说明 |
|--------|------|
| 客户端超时 | HTTP：`http.Client.Timeout`；gRPC：`context.WithTimeout`（已实现，可按环境调参）。 |
| 优雅关停 | `SIGINT`/`SIGTERM`：撤销 etcd 租约、关闭 gRPC/HTTP、`cmux` listener。 |
| etcd 续约失败恢复 | `KeepAlive` 通道关闭或超时后：指数退避重连、重新 `Grant` + `Put` + `KeepAlive`。 |
| gRPC 连接生命周期 | 长连接复用 stub（已有）；关停时统一 `Close`；可选连接池/限并发。 |
| 日志与错误 | `slog`/`zap`、结构化字段（`group`、`key` 可哈希）、区分 warn/error。 |

**建议周期：** 约 1～2 天。

---

## 阶段 2：缓存核心增强

**目标：** 体现对缓存语义、算法与常见故障模式的掌握。

| 工作项 | 说明 |
|--------|------|
| Hot cache（热点缓存） | 从非归属 peer 拿到的数据写入独立小 LRU，降低跨节点流量（groupcache 经典设计）。 |
| TTL / 过期 | 条目带过期时间；Get 时惰性淘汰；可选后台扫描。 |
| 穿透防护 | 布隆过滤器；或对「不存在」结果短 TTL 缓存（空值缓存）。 |
| 击穿与雪崩 | 已有 `singleflight`；补充单测；TTL 加随机抖动避免同时失效。 |
| 淘汰策略扩展 | 将 `lru` 抽象为 `Policy` 接口，可选 LFU、ARC 等，用 benchmark 对比。 |

**建议周期：** 约 3～5 天。

---

## 阶段 3：观测性

**目标：** 可量化、可排障；面试中易展开「你如何证明优化有效」。

| 工作项 | 说明 |
|--------|------|
| Prometheus | 已有基础指标；可扩展：`in_flight_requests`、按 `group`/错误类型拆分、`process_*`。 |
| pprof | 独立 debug 端口或受保护路径挂载 `net/http/pprof`。 |
| 分布式追踪 | OpenTelemetry + Jaeger：`Get`、peer 调用、Getter 各一段 span。 |
| 访问与 RPC 日志 | HTTP 中间件、gRPC 拦截器：request-id、耗时、状态码。 |

**建议周期：** 约 2～3 天。

---

## 阶段 4：一致性与数据面（进阶）

**目标：** 从只读缓存走向「写路径」与一致性取舍的讲解材料。

| 工作项 | 说明 |
|--------|------|
| `Set` / `Delete` API | HTTP + gRPC 协议扩展；写本地后广播或点对点失效。 |
| 失效传播 | etcd Watch、NATS、或 gossip 二选一做「版本号 / 失效事件」。 |
| 强一致（可选） | 小规模实验：基于 raft 的元数据或分片主副本（工作量大，选做）。 |
| 持久化与预热 | BoltDB/Badger 冷数据；启动时预热热点 key。 |
| 扩缩容与再均衡 | 一致性哈希变更时后台迁移或只读新环 + 双读过渡。 |

**建议周期：** 视深度而定，单项即可占 1～2 周。

---

## 阶段 5：客户端与易用性

**目标：** 让「使用者」视角完整，便于写 README 演示与集成测试。

| 工作项 | 说明 |
|--------|------|
| Go 客户端 SDK | 封装服务发现、超时、重试、可选一致性哈希客户端路由。 |
| 批量接口 | `MGet` 等，减少 RTT。 |
| 管理 REST | `/admin/peers`、`/admin/stats`、`/admin/flush?group=` 等（需鉴权或仅 bind localhost）。 |
| 简易控制台 | 节点拓扑、命中率、QPS（可用轻量前端或纯服务端模板）。 |

**建议周期：** 约 1～2 天（不含复杂 UI）。

---

## 阶段 6：工程化与交付形态

**目标：** 一键复现、CI 绿、贡献者友好。

| 工作项 | 说明 |
|--------|------|
| 容器化 | `Dockerfile`；`docker-compose`：多节点 + etcd +（可选）Prometheus/Grafana。 |
| 任务入口 | `Makefile` 或 `task`：`build`、`test`、`lint`、`proto`。 |
| 测试与基准 | `go test -race`；`consistenthash`/`lru`/`singleflight` 高覆盖；`BenchmarkGet`。 |
| CI | GitHub Actions：`vet`、`test -race`、`golangci-lint`。 |

**建议周期：** 约 1 天起，随 compose 复杂度增加。

---

## 推荐推进顺序（摘要）

在「基线」之上，若时间有限，建议按下面顺序做，每一步都能独立演示：

1. **阶段 1**（健壮性：关停、etcd 重注册、日志）— 部署相关故事完整。  
2. **阶段 3**（观测性：在已有 Prometheus 上补 pprof/Tracing）— 证明优化与排障能力。  
3. **阶段 2**（核心：hot cache + TTL + 穿透）— 体现缓存系统设计深度。  
4. **阶段 6**（docker-compose + CI）— 降低他人复现成本。  
5. **阶段 4 / 5** 按兴趣二选一深入（分布式写路径 **或** SDK + 管理接口）。

---

目标：5月5日左右第一版结束
