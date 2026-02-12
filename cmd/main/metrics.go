package main

import (
	"dex/consensus"
	"dex/logs"
	"dex/stats"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 接口调用统计结构体
type APICallStats struct {
	sync.RWMutex
	// 记录每个接口的累计调用次数
	CallCounts map[string]uint64
	// 记录每个节点每个接口的调用次数
	NodeCallCounts map[int]map[string]uint64
}

// 全局接口调用统计
var globalAPIStats = &APICallStats{
	CallCounts:     make(map[string]uint64),
	NodeCallCounts: make(map[int]map[string]uint64),
}

func monitorMetrics(nodes []*NodeInstance) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	// 用于记录每个节点上次的调用次数，计算增量
	lastCallCounts := make(map[int]map[string]uint64)

	for range ticker.C {
		// 临时存储当前周期的统计数据
		currentStats := make(map[string]uint64)
		nodeStats := make(map[int]map[string]uint64)

		for _, node := range nodes {
			if node == nil || node.SenderManager == nil {
				continue
			}

			// 1. 发送队列长度（控制面+数据面）
			sendQueueLen := node.SenderManager.SendQueue.QueueLen()

			// 2. 每目标在途请求
			node.SenderManager.SendQueue.InflightMutex.RLock()
			inflightCopy := make(map[string]int32)
			totalInflight := int32(0)
			for k, v := range node.SenderManager.SendQueue.InflightMap {
				inflightCopy[k] = v
				totalInflight += v
			}
			node.SenderManager.SendQueue.InflightMutex.RUnlock()

			// 3. 接收队列长度
			recvQueueLen := 0
			// if node.ConsensusManager != nil && node.ConsensusManager.Transport != nil {
			//     ...
			// }

			// 4. 接口调用统计（新增）
			if node.HandlerManager != nil {
				apiStats := node.HandlerManager.Stats.GetAPICallStats()

				// 记录当前节点的API调用统计
				nodeStats[node.ID] = apiStats

				// 计算增量并更新全局统计
				if lastCallCounts[node.ID] == nil {
					lastCallCounts[node.ID] = make(map[string]uint64)
				}

				for apiName, currentCount := range apiStats {
					// 计算这个周期的增量
					delta := currentCount
					if lastCount, exists := lastCallCounts[node.ID][apiName]; exists {
						delta = currentCount - lastCount
					}

					// 更新全局统计
					currentStats[apiName] += delta

					// 更新上次记录
					lastCallCounts[node.ID][apiName] = currentCount
				}
			}

			// 打印节点指标
			if sendQueueLen > 0 || totalInflight > 0 || recvQueueLen > 0 {
				fmt.Printf("[Metrics] Node %d: SendQ=%d, Inflight=%d, RecvQ=%d\n",
					node.ID, sendQueueLen, totalInflight, recvQueueLen)
			}
		}

		// 更新全局API调用统计
		globalAPIStats.Lock()
		for apiName, delta := range currentStats {
			globalAPIStats.CallCounts[apiName] += delta
		}
		for nodeID, apis := range nodeStats {
			globalAPIStats.NodeCallCounts[nodeID] = apis
		}
		globalAPIStats.Unlock()

		printAPICallStatistics()
	}
}

// 每 10s 打印所有队列状态（发送侧 + 接收侧 + 相关队列模块）
func monitorQueueStats(nodes []*NodeInstance) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		fmt.Printf("\n========== Queue Status @ %s ==========\n", time.Now().Format("15:04:05"))
		for _, node := range nodes {
			if node == nil {
				continue
			}

			channelStats := collectNodeQueueStats(node)
			sort.Slice(channelStats, func(i, j int) bool {
				if channelStats[i].Module != channelStats[j].Module {
					return channelStats[i].Module < channelStats[j].Module
				}
				return channelStats[i].Name < channelStats[j].Name
			})

			fmt.Printf("[Queue] Node %d (%s)\n", node.ID, node.Address)
			if len(channelStats) == 0 {
				fmt.Println("  (no queue stats available)")
				continue
			}

			for _, cs := range channelStats {
				fmt.Printf("  %-10s %-16s len=%5d cap=%5d usage=%6.2f%%\n",
					cs.Module, cs.Name, cs.Len, cs.Cap, cs.Usage*100)
			}

			// 额外打印发送侧在途请求数（非 channel，但对队列拥堵定位很关键）
			if node.SenderManager != nil && node.SenderManager.SendQueue != nil {
				sq := node.SenderManager.SendQueue
				sq.InflightMutex.RLock()
				inflightTargets := len(sq.InflightMap)
				totalInflight := int32(0)
				for _, v := range sq.InflightMap {
					totalInflight += v
				}
				sq.InflightMutex.RUnlock()
				fmt.Printf("  %-10s %-16s total=%5d targets=%5d\n",
					"SendQueue", "inflight", totalInflight, inflightTargets)
			}
		}
		fmt.Println("========================================")
	}
}

func collectNodeQueueStats(node *NodeInstance) []stats.ChannelStat {
	var result []stats.ChannelStat
	if node == nil {
		return result
	}

	if node.SenderManager != nil && node.SenderManager.SendQueue != nil {
		result = append(result, node.SenderManager.SendQueue.GetChannelStats()...)
	}
	if node.ConsensusManager != nil && node.ConsensusManager.Transport != nil {
		if rt, ok := node.ConsensusManager.Transport.(*consensus.RealTransport); ok {
			result = append(result, rt.GetChannelStats()...)
		}
	}
	if node.TxPool != nil {
		result = append(result, node.TxPool.GetChannelStats()...)
	}
	if node.DBManager != nil {
		result = append(result, node.DBManager.GetChannelStats()...)
	}

	return result
}

// 打印API调用统计
func printAPICallStatistics() {
	globalAPIStats.RLock()
	defer globalAPIStats.RUnlock()

	if len(globalAPIStats.CallCounts) == 0 {
		return
	}

	fmt.Println("\n========== API Call Statistics ==========")
	fmt.Println("Global API Call Counts:")

	// 按接口名称排序
	var apiNames []string
	for apiName := range globalAPIStats.CallCounts {
		apiNames = append(apiNames, apiName)
	}
	sort.Strings(apiNames)

	// 打印全局统计
	totalCalls := uint64(0)
	for _, apiName := range apiNames {
		count := globalAPIStats.CallCounts[apiName]
		totalCalls += count
		fmt.Printf("  %-30s: %10d calls\n", apiName, count)
	}
	fmt.Printf("  %-30s: %10d calls\n", "TOTAL", totalCalls)

	// 打印API调用频率分析
	fmt.Println("\nAPI Call Frequency Analysis:")
	if totalCalls > 0 {
		for _, apiName := range apiNames {
			count := globalAPIStats.CallCounts[apiName]
			percentage := float64(count) * 100.0 / float64(totalCalls)

			// 创建一个简单的条形图
			barLength := int(percentage / 2)
			if barLength > 40 {
				barLength = 40
			}
			bar := strings.Repeat("█", barLength)

			fmt.Printf("  %-25s: %6.2f%% %s\n", apiName, percentage, bar)
		}
	}

	fmt.Println("==========================================")
}

// 监控进度
func monitorProgress(nodes []*NodeInstance) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		fmt.Println("\n=== Consensus Progress ===")
		for _, node := range nodes {
			if node == nil || node.ConsensusManager == nil {
				continue
			}
			pref, height := node.ConsensusManager.GetLastAccepted()
			fmt.Printf("Node %d: Height=%d, LastAccepted=%s\n",
				node.ID, height, pref)
		}
		fmt.Println("==========================")
	}
}

// monitorMinerParticipantsByEpoch refreshes in-memory miner snapshot when epoch advances.
func monitorMinerParticipantsByEpoch(nodes []*NodeInstance) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		for _, node := range nodes {
			if node == nil || node.DBManager == nil || node.ConsensusManager == nil {
				continue
			}

			_, height := node.ConsensusManager.GetLastAccepted()
			if err := node.DBManager.RefreshMinerParticipantsByHeight(height); err != nil {
				logs.Debug("[MinerCacheMonitor] node=%d refresh failed: %v", node.ID, err)
			}
		}
	}
}
