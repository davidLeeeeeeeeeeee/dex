---
name: Configuration
description: System configuration management including node settings, FROST parameters, witness config, and genesis data.
triggers:
  - 配置
  - config
  - 参数
  - genesis
  - 初始化
---

# Config (配置) 模块指南

管理所有系统配置参数。

## 目录结构

```
config/
├── config.go              # ⭐ 主配置结构
├── frost.go               # FROST 参数
├── frost_default.json     # FROST 默认配置
├── witness.go             # 见证者参数
├── witness_default.json   # 见证者默认配置
└── genesis.json           # 创世配置
```

## 主配置 (config.go)

```go
type Config struct {
    Node     NodeConfig
    Database DatabaseConfig
    Network  NetworkConfig
    Consensus ConsensusConfig
    VM       VMConfig
}

type NodeConfig struct {
    Port        int
    DataDir     string
    LogLevel    string
    EnableFrost bool
}
```

## FROST 配置 (frost.go)

```go
type FrostConfig struct {
    Threshold       int     // 门限值 t
    TotalShares     int     // 总份额 n
    DKGWindowSize   int     // DKG 窗口大小（区块数）
    SignTimeout     time.Duration
    MaxRetries      int
}
```

## 见证者配置 (witness.go)

```go
type WitnessConfig struct {
    MinStake          string   // 最小质押
    VoteThreshold     float64  // 投票阈值
    ChallengePeriod   int      // 挑战期（区块数）
    ArbitrationThreshold float64
}
```

## Genesis 配置

```json
// genesis.json
{
  "initial_accounts": [
    {"address": "0x...", "balance": "1000000"}
  ],
  "initial_tokens": [
    {"symbol": "FB", "address": "0x...", "total_supply": "..."}
  ]
}
```

## 使用方式

```go
// 加载默认配置
cfg := config.DefaultConfig()

// 从文件加载
cfg, _ := config.LoadFromFile("config.json")

// 获取 FROST 配置
frostCfg := config.DefaultFrostConfig()
```

## 环境变量

支持通过环境变量覆盖配置：
```bash
DEX_NODE_PORT=8443
DEX_DATA_DIR=./data
DEX_LOG_LEVEL=debug
```

## 开发规范

1. **默认值**: 所有配置必须有合理默认值
2. **验证**: 加载后需验证配置有效性
3. **热更新**: 部分配置支持运行时更新

