package main

import (
	"dex/consensus"
	"dex/logs"
	"dex/stats"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type latencyEntry struct {
	Name    string
	Summary stats.LatencySummary
}

type gcSnapshot struct {
	takenAt       time.Time
	numGC         uint32
	pauseTotalNs  uint64
	totalAlloc    uint64
	heapAlloc     uint64
	heapInuse     uint64
	heapObjects   uint64
	nextGC        uint64
	mallocs       uint64
	frees         uint64
	gcCPUFraction float64
}

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

var gcMonitorOnce sync.Once

func monitorMetrics(nodes []*NodeInstance) {
	gcMonitorOnce.Do(func() {
		go monitorGCStats()
	})

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

				runtimeStats := sq.GetRuntimeStats()
				fmt.Printf("  %-10s %-16s timer=%5d sleep=%5d\n",
					"SendQueue", "nextAttempt",
					runtimeStats.DelayedTimerBacklog, runtimeStats.NextAttemptSleeping)
				fmt.Printf("  %-10s %-16s control=%5d data=%5d immediate=%5d\n",
					"SendQueue", "drop.queueFull",
					runtimeStats.DropControlFull, runtimeStats.DropDataFull, runtimeStats.DropImmediateFull)
				fmt.Printf("  %-10s %-16s stale=%5d overload=%5d requeue=%5d\n",
					"SendQueue", "drop/requeue",
					runtimeStats.DropStaleWorker, runtimeStats.DropStaleOverload, runtimeStats.InflightRequeue)
				fmt.Printf("  %-10s %-16s exhausted=%5d expired=%5d\n",
					"SendQueue", "retry.giveup",
					runtimeStats.RetryExhausted, runtimeStats.RetryExpired)
				fmt.Printf("  %-10s %-16s ok=%5d err=%5d timeout=%5d\n",
					"SendQueue", "send.result",
					runtimeStats.SendSuccess, runtimeStats.SendError, runtimeStats.SendTimeout)

				printLatencyTopN("SendQueue", sq.GetLatencyStats(true), 3, "sendqueue.do_send")
			}

			if node.ConsensusManager != nil && node.ConsensusManager.Node != nil {
				runtimeStats := node.ConsensusManager.Node.GetRuntimeStats()
				usage := 0.0
				if runtimeStats.SemCapacity > 0 {
					usage = float64(runtimeStats.SemInUse) / float64(runtimeStats.SemCapacity) * 100
				}
				fmt.Printf("  %-10s %-16s inUse=%5d cap=%5d peak=%5d usage=%6.2f%%\n",
					"HandleMsg", "sem",
					runtimeStats.SemInUse, runtimeStats.SemCapacity, runtimeStats.SemPeak, usage)

				printLatencyTopN("HandleMsg", node.ConsensusManager.Node.GetHandleMsgLatencyStats(true), 3, "node.handle_msg")
			}

			if node.ConsensusManager != nil && node.ConsensusManager.Transport != nil {
				if rt, ok := node.ConsensusManager.Transport.(*consensus.RealTransport); ok {
					runtimeStats := rt.GetRuntimeStats()
					fmt.Printf("  %-10s %-16s controlTimeout=%5d dataFull=%5d inboxTimeout=%5d invalid=%5d\n",
						"Transport", "drop",
						runtimeStats.ControlEnqueueTimeoutDrops,
						runtimeStats.DataEnqueueFullDrops,
						runtimeStats.InboxForwardTimeoutDrops,
						runtimeStats.PreprocessInvalidDrops)
				}
			}

			if node.HandlerManager != nil && node.HandlerManager.Stats != nil {
				printLatencyTopN("Handler", node.HandlerManager.Stats.GetLatencyStats(true), 5, "handler")
			}
		}
		fmt.Println("========================================")
	}
}

func captureGCSnapshot() gcSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return gcSnapshot{
		takenAt:       time.Now(),
		numGC:         ms.NumGC,
		pauseTotalNs:  ms.PauseTotalNs,
		totalAlloc:    ms.TotalAlloc,
		heapAlloc:     ms.HeapAlloc,
		heapInuse:     ms.HeapInuse,
		heapObjects:   ms.HeapObjects,
		nextGC:        ms.NextGC,
		mallocs:       ms.Mallocs,
		frees:         ms.Frees,
		gcCPUFraction: ms.GCCPUFraction,
	}
}

// monitorGCStats prints process-level GC pressure every 10s.
func monitorGCStats() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	prev := captureGCSnapshot()
	for range ticker.C {
		cur := captureGCSnapshot()

		seconds := cur.takenAt.Sub(prev.takenAt).Seconds()
		if seconds <= 0 {
			seconds = 1
		}

		allocRateMBs := float64(cur.totalAlloc-prev.totalAlloc) / seconds / (1024 * 1024)
		heapAllocMB := float64(cur.heapAlloc) / (1024 * 1024)
		heapInuseMB := float64(cur.heapInuse) / (1024 * 1024)
		nextGCMB := float64(cur.nextGC) / (1024 * 1024)
		heapDeltaMB := float64(int64(cur.heapAlloc)-int64(prev.heapAlloc)) / (1024 * 1024)
		objDelta := int64(cur.heapObjects) - int64(prev.heapObjects)
		mallocRate := float64(cur.mallocs-prev.mallocs) / seconds
		freeRate := float64(cur.frees-prev.frees) / seconds
		gcDelta := cur.numGC - prev.numGC
		pauseDeltaMs := float64(cur.pauseTotalNs-prev.pauseTotalNs) / float64(time.Millisecond)

		logs.Info(
			"[GC] 10s alloc=%.1fMB/s heap=%.1fMB(inuse=%.1fMB,delta=%+.1fMB) objs=%d(delta=%+d) malloc=%.0f/s free=%.0f/s gc=%d pause=%.2fms nextGC=%.1fMB gcCPU=%.2f%% goroutines=%d",
			allocRateMBs,
			heapAllocMB, heapInuseMB, heapDeltaMB,
			cur.heapObjects, objDelta,
			mallocRate, freeRate,
			gcDelta, pauseDeltaMs,
			nextGCMB, cur.gcCPUFraction*100,
			runtime.NumGoroutine(),
		)

		if allocRateMBs >= 256 || gcDelta >= 5 || pauseDeltaMs >= 200 {
			logs.Warn(
				"[GC] pressure alloc=%.1fMB/s gc=%d pause=%.2fms heap=%.1fMB objsDelta=%+d",
				allocRateMBs, gcDelta, pauseDeltaMs, heapAllocMB, objDelta,
			)
		}

		prev = cur
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

func printLatencyTopN(module string, summary map[string]stats.LatencySummary, topN int, prefix string) {
	top := selectTopLatency(summary, topN, prefix)
	if len(top) == 0 {
		return
	}
	for _, item := range top {
		fmt.Printf("  %-10s %-16s key=%s count=%5d p95=%-10v p99=%-10v max=%v\n",
			module, "latency",
			item.Name, item.Summary.Count, item.Summary.P95, item.Summary.P99, item.Summary.Max)
	}
}

func selectTopLatency(summary map[string]stats.LatencySummary, topN int, prefix string) []latencyEntry {
	if len(summary) == 0 || topN <= 0 {
		return nil
	}

	entries := make([]latencyEntry, 0, len(summary))
	for name, s := range summary {
		if s.Count == 0 {
			continue
		}
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		entries = append(entries, latencyEntry{Name: name, Summary: s})
	}
	if len(entries) == 0 {
		return nil
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Summary.P95 != entries[j].Summary.P95 {
			return entries[i].Summary.P95 > entries[j].Summary.P95
		}
		if entries[i].Summary.P99 != entries[j].Summary.P99 {
			return entries[i].Summary.P99 > entries[j].Summary.P99
		}
		return entries[i].Name < entries[j].Name
	})

	if len(entries) > topN {
		entries = entries[:topN]
	}
	return entries
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
