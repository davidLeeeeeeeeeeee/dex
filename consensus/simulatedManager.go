package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"dex/utils"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================
// 网络管理器
// ============================================

type NetworkManager struct {
	nodes      map[types.NodeID]*Node
	transports map[types.NodeID]interfaces.Transport
	config     *Config
	startTime  time.Time
	mu         sync.RWMutex
}

func (nm *NetworkManager) GetNodes() map[types.NodeID]*Node {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.nodes
}

func (nm *NetworkManager) GetConfig() *Config {
	return nm.config
}

func NewNetworkManager(config *Config) *NetworkManager {
	return &NetworkManager{
		nodes:      make(map[types.NodeID]*Node),
		transports: make(map[types.NodeID]interfaces.Transport),
		config:     config,
	}
}

func (nm *NetworkManager) CreateNodes() {
	byzantineMap := make(map[types.NodeID]bool)
	indices := rand.Perm(nm.config.Network.NumNodes)
	for i := 0; i < nm.config.Network.NumByzantineNodes; i++ {
		byzantineMap[types.NodeID(strconv.Itoa(indices[i]))] = true
	}

	ctx := context.Background()
	for i := 0; i < nm.config.Network.NumNodes; i++ {
		nodeID := types.NodeID(strconv.Itoa(i))
		transport := NewSimulatedTransport(nodeID, nm, ctx, nm.config.Network.NetworkLatency)
		nm.transports[nodeID] = transport
	}

	for i := 0; i < nm.config.Network.NumNodes; i++ {
		nodeID := types.NodeID(strconv.Itoa(i))
		// 为模拟场景创建 MemoryBlockStore
		store := NewMemoryBlockStore()

		// 为模拟节点生成 ECDSA 密钥对并注册公钥
		keyMgr := utils.NewKeyManager()
		if err := keyMgr.InitKeyRandom(); err != nil {
			logs.Error("[NetworkManager] Failed to generate ECDSA key for node %s: %v", nodeID, err)
			continue
		}
		RegisterNodePublicKey(nodeID, keyMgr.PublicKeyBytes())

		// Inject a new NodeLogger for each node
		node := NewNodeWithSigner(nodeID, nm.transports[nodeID], store, byzantineMap[nodeID], nm.config, logs.NewNodeLogger(string(nodeID), 2000), keyMgr)
		nm.nodes[nodeID] = node
	}
}

func (nm *NetworkManager) GetTransport(nodeID types.NodeID) interfaces.Transport {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.transports[nodeID]
}

func (nm *NetworkManager) SamplePeers(exclude types.NodeID, count int) []types.NodeID {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	peers := make([]types.NodeID, 0, len(nm.nodes)-1)
	for id := range nm.nodes {
		if id != exclude {
			peers = append(peers, id)
		}
	}

	rand.Shuffle(len(peers), func(i, j int) {
		peers[i], peers[j] = peers[j], peers[i]
	})

	if count > len(peers) {
		count = len(peers)
	}

	return peers[:count]
}

// GetAllPeers 返回所有已知节点（不含 exclude），用于 VRF 确定性采样
func (nm *NetworkManager) GetAllPeers(exclude types.NodeID) []types.NodeID {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	peers := make([]types.NodeID, 0, len(nm.nodes)-1)
	for id := range nm.nodes {
		if id != exclude {
			peers = append(peers, id)
		}
	}
	return peers
}

func (nm *NetworkManager) Start() {
	nm.startTime = time.Now()
	for _, node := range nm.nodes {
		node.Start()
	}
}

func (nm *NetworkManager) CheckProgress() (minHeight uint64, allDone bool) {
	minHeight = ^uint64(0)
	honestCount := 0

	for _, node := range nm.nodes {
		if !node.IsByzantine {
			honestCount++
			_, height := node.store.GetLastAccepted()
			if height < minHeight {
				minHeight = height
			}
		}
	}

	allDone = minHeight >= uint64(nm.config.Consensus.NumHeights)
	return minHeight, allDone
}

func (nm *NetworkManager) PrintStatus() {
	fmt.Println("\n===== Network Status =====")
	consensusMap := make(map[uint64]map[string]int)

	for id, node := range nm.nodes {
		lastAccepted, lastHeight := node.store.GetLastAccepted()
		currentHeight := node.store.GetCurrentHeight()

		nodeType := "Honest"
		if node.IsByzantine {
			nodeType = "Byzantine"
		}

		Logf("Node %d (%s): LastAccepted=%d, Current=%d, Block=%s\n",
			id, nodeType, lastHeight, currentHeight, lastAccepted)

		if lastHeight > 0 {
			if consensusMap[lastHeight] == nil {
				consensusMap[lastHeight] = make(map[string]int)
			}
			consensusMap[lastHeight][lastAccepted]++
		}
	}

	fmt.Println("\n--- Consensus by Height ---")
	for height := uint64(1); height <= uint64(nm.config.Consensus.NumHeights); height++ {
		if blocks, exists := consensusMap[height]; exists {
			Logf("Height %d: ", height)
			for blockID, count := range blocks {
				fmt.Printf("%s(%d nodes) ", blockID, count)
			}
			fmt.Println()
		}
	}
}

func (nm *NetworkManager) PrintFinalResults() {
	chains := make(map[types.NodeID][]string)

	for id, node := range nm.nodes {
		chain := make([]string, 0, nm.config.Consensus.NumHeights)
		for h := uint64(1); h <= uint64(nm.config.Consensus.NumHeights); h++ {
			if b, ok := node.store.GetFinalizedAtHeight(h); ok {
				chain = append(chain, b.ID)
			} else {
				chain = append(chain, "<none>")
			}
		}
		chains[id] = chain
	}

	allEqual := true
	var refChain []string
	for _, chain := range chains {
		if refChain == nil {
			refChain = chain
		} else {
			for i := range chain {
				if chain[i] != refChain[i] {
					allEqual = false
					break
				}
			}
		}
		if !allEqual {
			break
		}
	}

	fmt.Println("\n--- Global Agreement Check ---")
	if allEqual {
		Logf("All nodes have identical finalized chains: ✅ YES\n")
		fmt.Println("Consensus chain:")
		fmt.Println(strings.Join(refChain, " -> "))
	} else {
		Logf("All nodes have identical finalized chains: ❌ NO\n")
		for id, chain := range chains {
			nodeType := "Honest"
			if nm.nodes[id].IsByzantine {
				nodeType = "Byzantine"
			}
			Logf("Node %3d (%s): %s\n", id, nodeType, strings.Join(chain, " -> "))
		}
	}

	nm.PrintQueryStatistics()
}

func (nm *NetworkManager) PrintQueryStatistics() {
	fmt.Println("\n--- Query & Sync Statistics ---")

	totalQueriesSent := uint32(0)
	totalQueriesReceived := uint32(0)
	totalChitsResponded := uint32(0)
	totalGossipsReceived := uint32(0)
	totalBlocksProposed := uint32(0)

	queriesByHeight := make(map[uint64]uint32)

	honestNodeCount := 0
	for _, node := range nm.nodes {
		if node.IsByzantine {
			continue
		}
		honestNodeCount++

		node.Stats.Mu.Lock()
		totalQueriesSent += node.Stats.QueriesSent
		totalQueriesReceived += node.Stats.QueriesReceived
		totalChitsResponded += node.Stats.ChitsResponded
		totalGossipsReceived += node.Stats.GossipsReceived
		totalBlocksProposed += node.Stats.BlocksProposed

		for height, count := range node.Stats.QueriesPerHeight {
			queriesByHeight[height] += count
		}
		node.Stats.Mu.Unlock()
	}

	if honestNodeCount > 0 {
		avgQueriesSent := float64(totalQueriesSent) / float64(honestNodeCount)
		avgQueriesReceived := float64(totalQueriesReceived) / float64(honestNodeCount)
		avgChitsResponded := float64(totalChitsResponded) / float64(honestNodeCount)

		Logf("Average queries sent per honest node: %.2f\n", avgQueriesSent)
		Logf("Average queries received per honest node: %.2f\n", avgQueriesReceived)
		Logf("Average chits responded per honest node: %.2f\n", avgChitsResponded)
		Logf("Total blocks proposed: %d\n", totalBlocksProposed)
		Logf("Total gossips received: %d\n", totalGossipsReceived)

		fmt.Println("\nQueries per height (total across all honest nodes):")
		for h := uint64(1); h <= uint64(nm.config.Consensus.NumHeights); h++ {
			if count, exists := queriesByHeight[h]; exists {
				avgPerNode := float64(count) / float64(honestNodeCount)
				Logf("  Height %d: %d total queries (%.2f avg per node)\n", h, count, avgPerNode)
			}
		}

		totalHeightQueries := uint32(0)
		for _, count := range queriesByHeight {
			totalHeightQueries += count
		}
		if nm.config.Consensus.NumHeights > 0 {
			avgQueriesPerHeight := float64(totalHeightQueries) / float64(nm.config.Consensus.NumHeights)
			Logf("\nAverage queries per height (all nodes): %.2f\n", avgQueriesPerHeight)
			avgQueriesPerHeightPerNode := avgQueriesPerHeight / float64(honestNodeCount)
			Logf("Average queries per height per node: %.2f\n", avgQueriesPerHeightPerNode)
		}
	}
}
