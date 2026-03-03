package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	file := `d:\dex\vm\witness_handler.go`
	f, err := os.Open(file)
	if err != nil {
		panic(err)
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	replacements := map[int]string{
		22:  "// allocateVaultID 通过对 request_id 做哈希后取模，确定性地分配一个 vault_id。",
		23:  "// 算法：H(request_id) % vault_count，保证同一 request_id 始终映射到同一个 vault。",
		26:  "	// 取哈希前 4 字节转为 uint32",
		31:  "// allocateVaultIDWithLifecycleCheck 在分配 vault_id 时额外检查 Vault lifecycle。",
		32:  "// 若初始分配到的 Vault 处于 DRAINING 状态，则线性探测，找到第一个 ACTIVE Vault 返回。",
		39:  "	// 查询初始分配的 Vault 的 lifecycle 状态",
		45:  "			// 注意：这里读的是 VaultState.Status，不是 VaultTransitionState",
		46:  "			// 若该 Vault 处于 DRAINING，则线性探测找到第一个 ACTIVE Vault",
		48:  "				// 当前 Vault 处于 DRAINING，线性探测寻找 ACTIVE Vault",
		61:  "						// 该 Vault 数据不存在，视为未初始化，可分配（等同 ACTIVE）",
		65:  "				// 探测完所有 Vault 仍未找到 ACTIVE，所有 Vault 均处于 DRAINING",
		71:  "	// 初始分配的 Vault 不处于 DRAINING，直接返回（假定为 ACTIVE）",
		75:  "// WitnessServiceAware 是需要依赖 WitnessService 的 handler 接口。",
		76:  "// 实现该接口的 handler 可通过 SetWitnessService 注入 WitnessService 实例。",
		163: "// SetWitnessService 注入 WitnessService 实例。",
		201: "	// 读取见证人信息（不存在则初始化默认值）",
		246: "		// 质押：从可用余额扣除，转入 witness_locked_balance",
		258: "		// 累加质押金额到 witnessInfo.StakeAmount，并将状态设为 ACTIVE",
		303: "	// 将最新的 witnessInfo 热更新到 WitnessService 内存中",
		317: "// WitnessRequestTxHandler 处理见证入账请求交易，负责验证并记录充值请求。",
		323: "// SetWitnessService 注入 WitnessService 实例。",
		372: "	// 通过 WitnessService 创建充值请求（含见证轮次等业务逻辑）；",
		373: "	// 若未注入则直接构造基础结构体（测试/简易节点模式）",
		380: "		// 未注入 WitnessService 时的降级逻辑：直接构造最小充值请求结构体",
		435: "// WitnessVoteTxHandler 处理见证人投票交易，负责记录投票并更新充值请求状态。",
		440: "// SetWitnessService 注入 WitnessService 实例。",
		467: "		// 请求不存在：直接返回 FAILED receipt 而不返回 VM error，",
		468: "		// 防止整块交易 abort 影响后续交易。链上采用宽容策略：投票对应的请求若已不存在，视为冗余投票静默丢弃。",
		483: "	// 通过 WitnessService 处理投票业务逻辑（含阈值判断、请求状态流转等），",
		484: "	// 若未注入则手动累计计数（简易模式）",
		489: "			// WitnessService 处理投票失败：视为业务拒绝，返回 FAILED receipt（非 VM error）",
		528: "// WitnessChallengeTxHandler 处理见证挑战交易，在挑战期内对充值请求提起挑战。",
		533: "// SetWitnessService 注入 WitnessService 实例。",
	}

	out, err := os.Create(file)
	if err != nil {
		panic(err)
	}
	defer out.Close()

	for i, line := range lines {
		lineno := i + 1
		if rep, ok := replacements[lineno]; ok {
			fmt.Fprintln(out, rep)
		} else {
			fmt.Fprintln(out, line)
		}
	}
	fmt.Println("Done!")
}
