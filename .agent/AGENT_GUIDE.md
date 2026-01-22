# DEX Agent Guide

本文档帮助 AI Agent 快速理解项目结构并定位相关代码。

## 快速导航

### 按任务类型

| 任务类型 | 主要目录 | Skill |
|:---|:---|:---|
| 订单/撮合/成交 | `vm/order_handler.go`, `matching/` | vm, matching |
| DKG/签名/Vault | `frost/`, `vm/frost_*.go` | frost |
| 共识/区块/同步 | `consensus/` | consensus |
| API/接口 | `handlers/` | handlers |
| 前端/UI | `explorer/src/` | explorer |
| 数据存储 | `db/`, `keys/` | db |
| 见证/跨链 | `witness/`, `vm/witness_*.go` | witness |
| 配置 | `config/` | config |

### 按文件类型

| 后缀 | 位置 | 说明 |
|:---|:---|:---|
| `*.go` | 根目录各模块 | Go 后端代码 |
| `*.vue` | `explorer/src/` | Vue 前端组件 |
| `*.ts` | `explorer/src/` | TypeScript |
| `*.proto` | `pb/` | Protobuf 定义 |
| `*.json` | `config/` | 配置文件 |

## 核心文件索引

### 入口点
- `cmd/main.go` - 节点主入口
- `cmd/explorer/main.go` - Explorer 后端
- `explorer/src/App.vue` - 前端主入口

### 最复杂的文件 (修改需谨慎)
- `vm/order_handler.go` - 订单处理 (~900 行)
- `consensus/consensusEngine.go` - 共识引擎 (~800 行)
- `frost/runtime/transition_worker.go` - DKG 驱动 (~600 行)
- `handlers/handleOrderBook.go` - 订单簿 API (~500 行)

### Key 命名规范
所有数据库 Key 定义在 `keys/keys.go`，格式：`v1_<type>_<params>`

## Skills 目录

```
.agent/skills/
├── project_overview/  # 项目总览
├── vm/                # 虚拟机/交易执行
├── frost/             # 门限签名/DKG
├── consensus/         # Snowball 共识
├── matching/          # 撮合引擎
├── handlers/          # HTTP API
├── db/                # 数据库层
├── statedb/           # 状态数据库
├── witness/           # 见证系统
├── explorer/          # 前端
├── sender/            # 网络发送
├── txpool/            # 交易池
└── config/            # 配置管理
```

## 常见任务模板

### 1. 添加新 API 接口
1. 在 `handlers/` 添加处理函数
2. 在 `cmd/main.go` 注册路由
3. 在 `explorer/src/api.ts` 添加前端调用
4. 在 `explorer/src/types.ts` 添加类型定义

### 2. 添加新交易类型
1. 在 `pb/data.proto` 定义消息
2. 运行 `protoc --go_out=. pb/data.proto`
3. 在 `vm/` 创建 handler
4. 在 `vm/handlers.go` 注册

### 3. 修改订单簿逻辑
1. 阅读 `matching/match.go` 了解撮合
2. 修改 `vm/order_handler.go`
3. 更新 `handlers/handleOrderBook.go` 如需改 API
4. 运行 `go test ./vm/... ./matching/... -v`

### 4. 修改前端组件
1. 找到对应 `.vue` 文件
2. 检查 `api.ts` 是否有对应接口
3. 修改组件
4. 运行 `cd explorer && npm run dev` 测试

## 调试技巧

```bash
# 查看特定日志
grep "\[OrderHandler\]" logs/node.log
grep "\[DKG\]" logs/node.log

# 测试 API
curl "https://localhost:8443/status" -k
curl "https://localhost:8443/orderbook?pair=FB_USDT" -k

# 运行特定测试
go test ./vm/ -run TestOrderHandler -v
go test ./matching/ -run TestMatch -v
```

## 注意事项

1. **不要直接修改 `pb/data.pb.go`** - 这是生成的文件
2. **修改 Key 格式要同步 `keys/keys.go`** - 保持一致性
3. **VM 中不直接写数据库** - 通过 WriteOp 返回
4. **前端修改后需 `npm run build`** - 生产部署

