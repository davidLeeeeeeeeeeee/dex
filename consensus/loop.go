package consensus

import (
	"fmt"
	"math/rand"
	"time"
)

// ============================================
// 主函数
// ============================================

func RunLoop() {
	rand.Seed(time.Now().UnixNano())

	config := DefaultConfig()

	// 可以调整快照配置进行测试
	// config.Sync.SnapshotThreshold = 50  // 落后50个块就用快照
	// config.Snapshot.Interval = 50        // 每50个块创建一个快照

	fmt.Println("Starting Decoupled Snowman Consensus (With Snapshot Support)...")
	Logf("Network: %d nodes (%d honest, %d byzantine)\n",
		config.Network.NumNodes,
		config.Network.NumNodes-config.Network.NumByzantineNodes,
		config.Network.NumByzantineNodes)
	Logf("Heights: %d, Blocks per height: %d\n",
		config.Consensus.NumHeights,
		config.Consensus.BlocksPerHeight)

	if config.Snapshot.Enabled {
		Logf("Snapshot: Enabled (interval=%d, threshold=%d)\n",
			config.Snapshot.Interval,
			config.Sync.SnapshotThreshold)
	}

	network := NewNetworkManager(config)
	network.CreateNodes()

	programStart := time.Now()
	network.Start()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastHeight := uint64(0)
	for {
		select {
		case <-ticker.C:
			minHeight, allDone := network.CheckProgress()
			if minHeight > lastHeight {
				Logf("\n✅ All honest nodes reached consensus on height %d\n", minHeight)
				lastHeight = minHeight
			}

			if allDone {
				totalTime := time.Since(programStart)
				Logf("\n🎉 All heights completed! Total time: %v\n", totalTime)

				time.Sleep(1 * time.Second)

				fmt.Println("\n\n===== FINAL RESULTS =====")
				network.PrintStatus()
				network.PrintFinalResults()

				fmt.Println("\n--- Time Statistics ---")
				Logf("Total Time: %v\n", totalTime)
				Logf("Average/Height: %v\n", totalTime/time.Duration(config.Consensus.NumHeights))

				return
			}
		}
	}
}
