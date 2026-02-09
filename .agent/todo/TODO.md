# TODO

## 高优先级

- [ ] **存储分离 Phase 4**：移除 `SaveAccount` 中的 KV 双写，仅写 StateDB。参考 `artifacts/storage_separation_plan.md` 第四部分。
- [ ] **内存泄漏优化**：缩小 `SpecExecLRU` 缓存大小（`vm/spec_cache.go`），区块 finalize 后清除 `SpecResult.Diff`；缩小 `TxPool` 缓存。参考 pprof 40GB 堆分析结论。
- [ ] **Explorer txhistory 一致性**：修复 `height:3` 仍为 PENDING 而 `height:19` 为 SUCCEED 的非线性问题。涉及 `cmd/explorer/syncer/syncer.go` 的状态同步逻辑。

## 中优先级

- [ ] **编写数据迁移脚本**：清理 KV 中的冗余状态数据（Phase 4 配套）
- [ ] **Explorer 余额快照修复**：`syncer` 当前使用最新 nodeDB 状态而非历史高度状态，导致所有历史 `fb_balance_after` 相同
- [ ] **Consensus 防分叉加固**：`selectBestCandidate` 中低 window 偏好、`GetCachedBlock` 检查移除等修复已上线，需要长期观察稳定性

## 低优先级 / 持续维护

- [ ] Skill 文档与代码保持同步：如果重构移动了目录或新增了模块，更新 `.agent/skills/<module>/SKILL.md`
- [ ] 定期验证 `retrieval_index.md` 和 `AGENT_GUIDE.md` 中引用的路径仍然存在
- [ ] 考虑为 StateDB 读取添加缓存层（等系统运行瓶颈出现后再做）
