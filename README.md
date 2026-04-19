# GeeCache

一个轻量级的分布式缓存系统，参考 groupcache 的简化实现

## 特性

- **LRU 缓存淘汰策略** - 使用双向链表 + 哈希表实现
- **一致性哈希** - 解决数据在多节点间的分布与负载均衡
- **单节点缓存** - 本地缓存 + 数据源回调
- **分布式通信** - HTTP 协议实现节点间通信
- **命名空间** - Group 机制隔离不同业务缓存

## 项目结构

```
geecache/
├── consistenthash/   # 一致性哈希实现
│   └── consistenthash.go
├── geecache/       # 核心包
│   ├── byteview.go  # 缓存值的只读视图
│   ├── group.go     # 缓存组与核心逻辑
│   └── http.go      # HTTP 服务与客户端
├── lru/            # LRU 缓存实现
│   └── cache.go
├── go.mod
├── main.go         # 示例程序
└── README.md
```

## 快速开始

### 1. 启动服务

```bash
# 启动节点 1 (端口 8001)
go run main.go -port 8001

# 启动节点 2 (端口 8002)
go run main.go -port 8002

# 启动节点 3 (端口 8003)
go run main.go -port 8003
```

### 2. 测试缓存

```bash
curl http://localhost:8001/geecache/scores/Tom
```

## 可供使用的数据示例


"Tom":  "630",
"Jack": "589",
"Sam":  "567",


## 核心组件说明

### 1. LRU 缓存

`lru.Cache` 实现了 LRU（Least Recently Used）缓存淘汰策略：
- 使用双向链表维护访问顺序
- 使用哈希表快速查找
- 支持容量限制与淘汰回调

### 2. 一致性哈希

`consistenthash.Map` 实现了一致性哈希算法：
- 虚拟节点机制，解决数据倾斜问题
- 支持动态添加/删除节点
- 哈希函数可配置

### 3. HTTP 通信

`geecache.HTTPPool 实现了节点间的 HTTP 通信：
- ServeHTTP 处理请求路由
- PickPeer 选择合适的节点
- httpGetter 远程获取数据

### 4. Group 缓存组

`geecache.Group` 是核心命名空间：
- 本地缓存 + 数据源回调
- 自动从远程节点获取数据
- 缓存未命中时自动回源

## 许可证

MIT License

## 待做事项
- 增加并发测试
- 支持gRPC协议通信
- 实现节点故障自动摘除
- 添加单元测试

