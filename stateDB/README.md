设计要点（精简）

数据模型（仅同步用）

快照层（SNAP）：在高度 E（整除 40,000）产出“虚拟快照”。不做整库全量拷贝，而是：

首个快照可做全量（在线扫描，不暂停）

之后快照采用 Base + Overlay：以上一个快照为基底，仅为“本 Epoch 改过的键”写 overlay（极大减轻 IO 与磁盘压力）

内存 diff（DIFF_MEM）：[E+1, E+40000] 的变更都进内存窗口（支持 shard 化 & 分页导出）。若担心宕机丢失，可开启可选 WAL，按块滚动写入（TTL=一个 Epoch）。

分片与分页

一级前缀：v1_account_（与现状一致，方便筛出 account 键）

二级分片：对 addr 做 sha256([]byte(addr))，取前 N 个十六进制字符（N=1 => 16 片；N=2 => 256 片），键形如：
s1|snap|<E>|<shard>|<addr> ；s1|ovl|<E>|<shard>|<addr>

分页：每个 shard 内按字典序分页，返回 total_pages / page / page_size，并给 next_page_token（基于“最后一个 key”的 seek token）。

与主库交互

你的 db/manage_account.go 在修改账户时，同时调用 stateDB.ApplyAccountUpdate(height, key, val)

Epoch 结束（或检测到高度跨越 E+40000）调用 stateDB.SnapshotIfNeeded(curHeight)：

把内存 diff 固化为 overlay

记录快照元数据（按 shard 的计数、覆盖范围等）

清空当前 Epoch 的内存窗口

版本与高度映射

Badger 的 Ts 不作为核心索引，只用于留存 10 个版本的元信息等；

统一用 Badger Sequence 生成单调递增的提交序号：

meta:h2seq:<height> => seq

meta:seq2h:<seq> => height
查询或审计时就能按高度精确定位到对应的提交点。

新节点同步流程（示例）

目标高度 H，计算 E = floor(H/40000)*40000

并行从多个矿工按 shard + 分页拉取：

SNAP(E) 的 Base（如果 E 是首个快照则全量，否则拉取 Base+Overlay 链，链长一般 1；为防链过长，可每 N 个 Epoch 做一次全量快照）

DIFF(E+1 .. H) 的 内存 diff（可跨多矿工并行拉）

本地合并：Base → 依次应用 overlay → 应用 diff，得到 H 时刻的同步态。