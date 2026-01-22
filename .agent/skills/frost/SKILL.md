---
name: FROST & DKG
description: Guide for understanding and modifying the FROST threshold signature and DKG (Distributed Key Generation) modules.
---

# FROST & DKG 模块指南

该模块负责系统的核心安全：门限签名 (FROST/ROAST) 和分布式密钥生成 (DKG)。

## 目录结构
-   `frost/design.md`: 模块设计文档
-   `core/`: 纯数学/算法实现 (Curves, DKG, Schnorr)。
-   `runtime/`: 异步背景服务 (Scanner, Workers)。
    -   `transition_worker.go`: 驱动 DKG 状态机 (Commit -> Share -> Validation)。
    -   `withdraw_worker.go`: 驱动提现签名流程。
-   `chain/`: 链适配器 (BTC, EVM, TRON)。
-   `session/`: 会话持久化和 Nonce 管理。

## 关键流程：DKG (Distributed Key Generation)

DKG 流程严格依赖区块高度驱动的窗口，共分为 4 个主要阶段：

1.  **Commit**: 参与者发布承诺。
2.  **Share**: 参与者交换加密份额。
3.  **Validation**: 确认所有人已收到份额。
4.  **Ready**: 密钥生成完成，Vault 激活。

**注意点**: DKG 必须在 `transition_worker.go` 中等待特定的 `BlockHeight` 窗口。

## 关键文件

-   `d:\dex\frost\runtime\transition_worker.go`: DKG 执行核心。
-   `d:\dex\vm\frost_dkg_handlers.go`: 链上状态转换逻辑。
-   `d:\dex\frost\core\frost\threshold.go`: FROST 聚合算法。

## 开发规范

-   **不要在 Core 模块引入外界依赖**: `core/` 应该是纯计算的。
-   **高度敏感**: 任何交易提交必须检查当前高度是否在生命周期窗口内。
-   **日志**: 使用 `[TransitionWorker]` 或 `[DKG]` 前缀。

## 常见调试任务

-   **检查为何不提交 DKG 交易**: 查看 `transition_worker.go` 的 `isHeightInWindow` 检查。
-   **模拟签名失败**: 在 `participant.go` 中拦截 `ProcessSignRequest`。
