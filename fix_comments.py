import re

with open(r'd:\dex\vm\witness_handler.go', 'r', encoding='utf-8') as f:
    lines = f.readlines()

# 逐行处理：只要是注释行且含非 ASCII 乱码字符，就整体替换为正确中文
# 按行号对应关系做替换（以行号为锚点）
replacements = {
    22: "// allocateVaultID 通过对 request_id 做哈希后取模，确定性地分配一个 vault_id。\r\n",
    23: "// 算法：H(request_id) % vault_count，保证同一 request_id 始终映射到同一个 vault。\r\n",
    26: "\t// 取哈希前 4 字节转为 uint32\r\n",
    31: "// allocateVaultIDWithLifecycleCheck 在分配 vault_id 时额外检查 Vault lifecycle。\r\n",
    32: "// 若初始分配到的 Vault 处于 DRAINING 状态，则线性探测，找到第一个 ACTIVE Vault 返回。\r\n",
    39: "\t// 查询初始分配的 Vault 的 lifecycle 状态\r\n",
    45: "\t\t\t// 注意：这里读的是 VaultState.Status，不是 VaultTransitionState\r\n",
    46: "\t\t\t// 若该 Vault 处于 DRAINING，则线性探测找到第一个 ACTIVE Vault\r\n",
    48: "\t\t\t\t// 当前 Vault 处于 DRAINING，线性探测寻找 ACTIVE Vault\r\n",
    61: "\t\t\t\t\t\t// 该 Vault 数据不存在，视为未初始化，可分配（等同 ACTIVE）\r\n",
    71: "\t// 初始分配的 Vault 不处于 DRAINING，直接返回（假定为 ACTIVE）\r\n",
    75: "// WitnessServiceAware 是需要依赖 WitnessService 的 handler 接口。\r\n",
    76: "// 实现该接口的 handler 可通过 SetWitnessService 注入 WitnessService 实例。\r\n",
    163: "// SetWitnessService 注入 WitnessService 实例。\r\n",
    201: "\t// 读取见证人信息（不存在则初始化默认值）\r\n",
    246: "\t\t// 质押：从可用余额扣除，转入 witness_locked_balance\r\n",
    258: "\t\t// 累加质押金额到 witnessInfo.StakeAmount，并将状态设为 ACTIVE\r\n",
    303: "\t// 将最新的 witnessInfo 热更新到 WitnessService 内存中\r\n",
    317: "// WitnessRequestTxHandler 处理见证入账请求交易，负责验证并记录充值请求。\r\n",
    323: "// SetWitnessService 注入 WitnessService 实例。\r\n",
    372: "\t// 通过 WitnessService 创建充值请求（含见证轮次等业务逻辑）；\r\n",
    380: "\t\t// 未注入 WitnessService 时的降级逻辑：直接构造最小充值请求结构体\r\n",
    435: "// WitnessVoteTxHandler 处理见证人投票交易，负责记录投票并更新充值请求状态。\r\n",
    440: "// SetWitnessService 注入 WitnessService 实例。\r\n",
    467: "\t\t// 请求不存在：直接返回 FAILED receipt 而不返回 VM error，防止整块交易 abort\r\n",
    468: "\t\t// 影响后续交易。链上采用宽容策略：投票对应的请求若已不存在，视为冗余投票静默丢弃。\r\n",
    483: "\t// 通过 WitnessService 处理投票业务逻辑（含阈值判断、请求状态流转等）；若未注入则手动累计计数（简易模式）\r\n",
    489: "\t\t\t// WitnessService 处理投票失败：视为业务拒绝，返回 FAILED receipt（非 VM error）\r\n",
    528: "// WitnessChallengeTxHandler 处理见证挑战交易，在挑战期内对充值请求提起挑战。\r\n",
    533: "// SetWitnessService 注入 WitnessService 实例。\r\n",
}

# 373行：未注入WitnessService时直接构造充值请求，这行后面还需要删掉重复注释
# 按 1-indexed 行号替换
result = []
for i, line in enumerate(lines):
    lineno = i + 1  # 1-indexed
    if lineno in replacements:
        result.append(replacements[lineno])
    else:
        result.append(line)

with open(r'd:\dex\vm\witness_handler.go', 'w', encoding='utf-8') as f:
    f.writelines(result)

print("Done. Modified", len(replacements), "lines.")
