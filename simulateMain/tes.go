package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"dex/consensus"
	"dex/logs"
	"dex/pb"
	"dex/stats"
	"dex/types"
	"encoding/pem"
	"fmt"
	"math/big"
	mathrand "math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"sort"
	"strings"

	"github.com/quic-go/quic-go/http3"
	"google.golang.org/protobuf/proto"
)

// 每个节点的 API 统计
var (
	nodeStatsMap   = make(map[types.NodeID]*stats.Stats)
	nodeStatsMapMu sync.RWMutex
)

// APICallStats 接口调用统计结构体
type APICallStats struct {
	sync.RWMutex
	// 记录每个接口的累计调用次数
	CallCounts map[string]uint64
}

// 全局接口调用统计
var globalAPIStats = &APICallStats{
	CallCounts: make(map[string]uint64),
}

// getOrCreateNodeStats 获取或创建节点的统计实例
func getOrCreateNodeStats(nodeID types.NodeID) *stats.Stats {
	nodeStatsMapMu.RLock()
	s, ok := nodeStatsMap[nodeID]
	nodeStatsMapMu.RUnlock()
	if ok {
		return s
	}

	nodeStatsMapMu.Lock()
	defer nodeStatsMapMu.Unlock()
	// double check
	if s, ok = nodeStatsMap[nodeID]; ok {
		return s
	}
	s = stats.NewStats()
	nodeStatsMap[nodeID] = s
	return s
}

func main() {
	mathrand.Seed(time.Now().UnixNano())

	config := consensus.DefaultConfig()
	config.Consensus.NumHeights = 50
	config.Consensus.BlocksPerHeight = 5

	fmt.Println("Starting Enhanced Simulated Consensus with HTTP/3 Web Monitor...")

	network := consensus.NewNetworkManager(config)
	network.CreateNodes()

	// 为每个节点启动 HTTP/3 服务器
	nodes := network.GetNodes()
	for id, node := range nodes {
		port := 6000 + int(id.Last2Mod100())
		// 注册映射，方便 Explorer 通过 host:port 找到 NodeID 进而找到内存日志
		logs.RegisterNodeMapping(fmt.Sprintf("127.0.0.1:%d", port), string(node.ID))
		logs.RegisterNodeMapping(string(node.ID), string(node.ID))

		go startNodeWeb(node, port)
	}

	programStart := time.Now()
	network.Start()

	// 启动 API 统计监控
	go monitorMetrics(network)

	// 监控模拟进度
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		lastHeight := uint64(0)
		for range ticker.C {
			minHeight, allDone := network.CheckProgress()
			if minHeight > lastHeight {
				fmt.Printf("\n✅ All honest nodes reached consensus on height %d\n", minHeight)
				lastHeight = minHeight
			}
			if allDone {
				totalTime := time.Since(programStart)
				fmt.Printf("\n🎉 All heights completed! Total time: %v\n", totalTime)
				fmt.Println("Keep running to allow Web Monitor access...")
				network.PrintStatus()
				network.PrintFinalResults()
				return
			}
		}
	}()

	// 等待退出信号，不要完成模拟就退出，否则网页会 timeout
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	fmt.Println("\n🛑 Shutting down...")
}

func startNodeWeb(node *consensus.Node, port int) {
	mux := http.NewServeMux()

	// 获取该节点的统计实例
	nodeStats := getOrCreateNodeStats(node.ID)

	// 1. 状态接口
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		nodeStats.RecordAPICall("HandleStatus")
		blockID, height := node.GetLastAccepted()
		resProto := &pb.StatusResponse{
			Status: "ok",
			Info:   fmt.Sprintf("Simulated Node %s (Height: %d, Block: %s)", node.ID, height, blockID),
		}
		sendProto(w, resProto)
	})

	// 2. 高度接口 (Explorer 强依赖且会循环调用)
	mux.HandleFunc("/heightquery", func(w http.ResponseWriter, r *http.Request) {
		nodeStats.RecordAPICall("HandleHeightQuery")
		_, height := node.GetLastAccepted()
		resProto := &pb.HeightResponse{
			CurrentHeight:      height,
			LastAcceptedHeight: height,
			Address:            string(node.ID),
		}
		sendProto(w, resProto)
	})

	// 3. 获取区块详情 (详情页依赖)
	mux.HandleFunc("/getblock", func(w http.ResponseWriter, r *http.Request) {
		nodeStats.RecordAPICall("HandleGetBlock")
		blockID, height := node.GetLastAccepted()
		miner := string(node.ID)

		// 尝试从 Store 获取真实区块以提取真实的 Proposer (Miner)
		if b, ok := node.GetBlock(blockID); ok {
			miner = b.Proposer
		}

		resProto := &pb.GetBlockResponse{
			Block: &pb.Block{
				Height:    height,
				BlockHash: blockID,
				Miner:     miner,
			},
		}
		sendProto(w, resProto)
	})

	// 4. 获取最近区块
	mux.HandleFunc("/getrecentblocks", func(w http.ResponseWriter, r *http.Request) {
		nodeStats.RecordAPICall("HandleGetRecentBlocks")
		blockID, height := node.GetLastAccepted()
		miner := string(node.ID)

		if b, ok := node.GetBlock(blockID); ok {
			miner = b.Proposer
		}

		resProto := &pb.GetRecentBlocksResponse{
			Blocks: []*pb.BlockHeader{
				{
					Height:  height,
					Miner:   miner,
					TxCount: 0,
				},
			},
		}
		sendProto(w, resProto)
	})

	// 5. 日志接口
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		nodeStats.RecordAPICall("HandleLogs")
		logLines := logs.GetLogsForNode(string(node.ID))
		resp := &pb.LogsResponse{
			Logs: logLines,
		}
		sendProto(w, resp)
	})

	// 6. Metrics 接口（真实统计数据）
	mux.HandleFunc("/frost/metrics", func(w http.ResponseWriter, r *http.Request) {
		nodeStats.RecordAPICall("GetMetrics")

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		// 获取 HTTP API 调用统计
		apiStats := nodeStats.GetAPICallStats()

		// 合并共识消息处理统计（PushQuery, PullQuery, Chits, Gossip 等）
		msgStats := node.GetMessageStats()
		for k, v := range msgStats {
			apiStats[k] = v
		}

		resp := &pb.MetricsResponse{
			HeapAlloc:      m.HeapAlloc,
			HeapSys:        m.HeapSys,
			NumGoroutine:   int32(runtime.NumGoroutine()),
			FrostJobs:      0,
			FrostWithdraws: 0,
			ApiCallStats:   apiStats,
		}
		sendProto(w, resp)
	})

	addr := fmt.Sprintf(":%d", port)

	// 设置 HTTP/3 服务器 (Explorer 必须 HTTPS + HTTP/3)
	certFile := fmt.Sprintf("sim_node_%s.crt", node.ID)
	keyFile := fmt.Sprintf("sim_node_%s.key", node.ID)
	generateSelfSignedCert(certFile, keyFile)
	defer os.Remove(certFile)
	defer os.Remove(keyFile)

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h3", "h3-29", "h3-28", "h3-27"},
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		fmt.Printf("❌ Node %s: Failed to load TLS cert: %v\n", node.ID, err)
		return
	}
	tlsConfig.Certificates = []tls.Certificate{cert}

	server := &http3.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: tlsConfig,
	}

	fmt.Printf("📡 Node %s Web Monitor (HTTP/3): https://127.0.0.1%s\n", node.ID, addr)
	if err := server.ListenAndServe(); err != nil {
		fmt.Printf("❌ Node %s: Web server failed: %v\n", node.ID, err)
	}
}

// monitorMetrics 定期监控各个节点的 API 和共识消息调用情况
func monitorMetrics(network *consensus.NetworkManager) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	// 用于记录每个节点上次的调用次数，计算整个周期的增量
	lastCallCounts := make(map[types.NodeID]map[string]uint64)

	for range ticker.C {
		// 临时存储当前周期的统计数据
		currentStats := make(map[string]uint64)
		nodes := network.GetNodes()

		for nodeID, node := range nodes {
			if node == nil {
				continue
			}

			// 1. 获取 HTTP API 调用统计
			nodeStats := getOrCreateNodeStats(nodeID)
			apiStats := nodeStats.GetAPICallStats()

			// 2. 合并共识消息处理统计
			msgStats := node.GetMessageStats()
			for k, v := range msgStats {
				apiStats[k] = v
			}

			// 3. 计算增量
			if lastCallCounts[nodeID] == nil {
				lastCallCounts[nodeID] = make(map[string]uint64)
			}

			for apiName, currentCount := range apiStats {
				delta := currentCount
				if lastCount, exists := lastCallCounts[nodeID][apiName]; exists {
					delta = currentCount - lastCount
				}

				// 更新全局当前周期的增量统计
				currentStats[apiName] += delta

				// 更新这个节点的上次记录
				lastCallCounts[nodeID][apiName] = currentCount
			}
		}

		// 更新全局累计 API 调用统计
		globalAPIStats.Lock()
		for apiName, delta := range currentStats {
			globalAPIStats.CallCounts[apiName] += delta
		}
		globalAPIStats.Unlock()

		printAPICallStatistics()
	}
}

// printAPICallStatistics 打印 API 调用统计
func printAPICallStatistics() {
	globalAPIStats.RLock()
	defer globalAPIStats.RUnlock()

	if len(globalAPIStats.CallCounts) == 0 {
		return
	}

	fmt.Println("\n========== Consensus / API Call Statistics ==========")
	fmt.Println("Global Call Counts (Total):")

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
	fmt.Printf("  %-30s: %d calls\n", "TOTAL", totalCalls)

	// 打印分析
	fmt.Println("\nCall Frequency Analysis:")
	if totalCalls > 0 {
		for _, apiName := range apiNames {
			count := globalAPIStats.CallCounts[apiName]
			percentage := float64(count) * 100.0 / float64(totalCalls)

			// 条形图展示
			barLength := int(percentage / 2)
			if barLength > 40 {
				barLength = 40
			}
			bar := strings.Repeat("█", barLength)

			fmt.Printf("  %-25s: %6.2f%% %s\n", apiName, percentage, bar)
		}
	}

	fmt.Println("=====================================================")
}

func sendProto(w http.ResponseWriter, msg proto.Message) {
	data, _ := proto.Marshal(msg)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}

func generateSelfSignedCert(certFile, keyFile string) error {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"localhost"},
	}
	certDER, _ := x509.CreateCertificate(cryptorand.Reader, &template, &template, &priv.PublicKey, priv)

	cOut, _ := os.Create(certFile)
	defer cOut.Close()
	pem.Encode(cOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	kOut, _ := os.Create(keyFile)
	defer kOut.Close()
	privBytes, _ := x509.MarshalECPrivateKey(priv)
	pem.Encode(kOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})
	return nil
}
