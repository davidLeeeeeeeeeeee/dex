package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dex/config"
	"dex/logs"
	"dex/pb"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"google.golang.org/protobuf/proto"
)

type server struct {
	client       *http.Client
	timeout      time.Duration
	defaultNodes []string
	basePort     int
	count        int
}

type nodesResponse struct {
	BasePort int      `json:"base_port"`
	Count    int      `json:"count"`
	Nodes    []string `json:"nodes"`
}

type summaryRequest struct {
	Nodes        []string `json:"nodes"`
	IncludeBlock bool     `json:"include_block"`
	IncludeFrost bool     `json:"include_frost"`
}

type summaryResponse struct {
	GeneratedAt string        `json:"generated_at"`
	Nodes       []nodeSummary `json:"nodes"`
	Errors      []string      `json:"errors,omitempty"`
	Selected    []string      `json:"selected"`
	ElapsedMs   int64         `json:"elapsed_ms"`
}

type nodeSummary struct {
	Address            string        `json:"address"`
	Status             string        `json:"status,omitempty"`
	Info               string        `json:"info,omitempty"`
	CurrentHeight      uint64        `json:"current_height,omitempty"`
	LastAcceptedHeight uint64        `json:"last_accepted_height,omitempty"`
	LatencyMs          int64         `json:"latency_ms,omitempty"`
	Error              string        `json:"error,omitempty"`
	Block              *blockSummary `json:"block,omitempty"`
	FrostMetrics       *frostMetrics `json:"frost_metrics,omitempty"`
}

type nodeDetails struct {
	nodeSummary
	Logs         []*pb.LogLine     `json:"logs,omitempty"`
	RecentBlocks []*pb.BlockHeader `json:"recent_blocks,omitempty"`
}

type blockSummary struct {
	Height        uint64         `json:"height"`
	BlockHash     string         `json:"block_hash,omitempty"`
	PrevBlockHash string         `json:"prev_block_hash,omitempty"`
	TxsHash       string         `json:"txs_hash,omitempty"`
	Miner         string         `json:"miner,omitempty"`
	TxCount       int            `json:"tx_count"`
	TxTypeCounts  map[string]int `json:"tx_type_counts,omitempty"`
	Accumulated   string         `json:"accumulated_reward,omitempty"`
	Window        int32          `json:"window,omitempty"`
}

type frostMetrics struct {
	HeapAlloc      uint64 `json:"heap_alloc"`
	HeapSys        uint64 `json:"heap_sys"`
	NumGoroutine   int32  `json:"num_goroutine"`
	FrostJobs      int32  `json:"frost_jobs"`
	FrostWithdraws int32  `json:"frost_withdraws"`
}

// Block/Tx search request/response types
type blockRequest struct {
	Node   string `json:"node"`
	Height uint64 `json:"height,omitempty"`
	Hash   string `json:"hash,omitempty"`
}

type blockResponse struct {
	Block *blockInfo `json:"block,omitempty"`
	Error string     `json:"error,omitempty"`
}

type blockInfo struct {
	Height      uint64      `json:"height"`
	BlockHash   string      `json:"block_hash"`
	PrevHash    string      `json:"prev_block_hash,omitempty"`
	TxsHash     string      `json:"txs_hash,omitempty"`
	Miner       string      `json:"miner,omitempty"`
	TxCount     int         `json:"tx_count"`
	Accumulated string      `json:"accumulated_reward,omitempty"`
	Window      int32       `json:"window,omitempty"`
	Txs         []txSummary `json:"transactions,omitempty"`
}

type txSummary struct {
	TxID        string `json:"tx_id"`
	TxType      string `json:"tx_type,omitempty"`
	FromAddress string `json:"from_address,omitempty"`
	Status      string `json:"status,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

type txRequest struct {
	Node string `json:"node"`
	TxID string `json:"tx_id"`
}

type txResponse struct {
	Transaction *txInfo `json:"transaction,omitempty"`
	Error       string  `json:"error,omitempty"`
}

type txInfo struct {
	TxID           string                 `json:"tx_id"`
	TxType         string                 `json:"tx_type,omitempty"`
	FromAddress    string                 `json:"from_address,omitempty"`
	ToAddress      string                 `json:"to_address,omitempty"`
	Status         string                 `json:"status,omitempty"`
	ExecutedHeight uint64                 `json:"executed_height,omitempty"`
	Fee            string                 `json:"fee,omitempty"`
	Nonce          uint64                 `json:"nonce,omitempty"`
	Details        map[string]interface{} `json:"details,omitempty"`
}

func main() {
	cfg := config.DefaultConfig()

	listenAddr := flag.String("listen", "127.0.0.1:8080", "Explorer HTTP listen address")
	host := flag.String("host", "127.0.0.1", "Default node host")
	basePort := flag.Int("base-port", cfg.Network.BasePort, "Base port for node list")
	count := flag.Int("count", cfg.Network.DefaultNumNodes, "Number of nodes")
	nodesFlag := flag.String("nodes", "", "Comma-separated node addresses (host:port)")
	webDirFlag := flag.String("web-dir", "", "Path to explorer static files")
	timeout := flag.Duration("timeout", 3*time.Second, "Per-request timeout")
	flag.Parse()

	defaultNodes := buildNodes(*nodesFlag, *host, *basePort, *count)
	webDir := resolveWebDir(*webDirFlag)
	if webDir == "" {
		log.Fatal("unable to locate explorer web directory, use --web-dir")
	}

	client, transport := newHTTP3Client(*timeout)
	defer transport.Close()

	srv := &server{
		client:       client,
		timeout:      *timeout,
		defaultNodes: defaultNodes,
		basePort:     *basePort,
		count:        *count,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/nodes", srv.handleNodes)
	mux.HandleFunc("/api/summary", srv.handleSummary)
	mux.HandleFunc("/api/node/details", srv.handleNodeDetails)
	mux.HandleFunc("/api/block", srv.handleBlock)
	mux.HandleFunc("/api/tx", srv.handleTx)
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	log.Printf("Explorer listening at http://%s (ui: %s)", *listenAddr, webDir)
	if err := http.ListenAndServe(*listenAddr, logRequests(mux)); err != nil {
		log.Fatal(err)
	}
}

func buildNodes(nodesFlag, host string, basePort, count int) []string {
	if strings.TrimSpace(nodesFlag) != "" {
		return normalizeNodes(strings.Split(nodesFlag, ","))
	}
	nodes := make([]string, 0, count)
	for i := 0; i < count; i++ {
		nodes = append(nodes, fmt.Sprintf("%s:%d", host, basePort+i))
	}
	return nodes
}

func normalizeNodes(nodes []string) []string {
	unique := make(map[string]struct{})
	var out []string
	for _, node := range nodes {
		n := strings.TrimSpace(node)
		if n == "" {
			continue
		}
		n = strings.TrimPrefix(n, "https://")
		n = strings.TrimPrefix(n, "http://")
		n = strings.TrimSuffix(n, "/")
		if _, exists := unique[n]; exists {
			continue
		}
		unique[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func resolveWebDir(explicit string) string {
	if explicit != "" {
		if dirExists(explicit) {
			return explicit
		}
		return ""
	}
	// 优先使用构建后的 dist 目录，其次是 explorer 源目录（开发时）
	candidates := []string{
		filepath.Join("explorer", "dist"),
		filepath.Join("..", "explorer", "dist"),
		filepath.Join("..", "..", "explorer", "dist"),
		"explorer",
		filepath.Join("..", "explorer"),
		filepath.Join("..", "..", "explorer"),
	}
	for _, candidate := range candidates {
		if dirExists(candidate) {
			return candidate
		}
	}
	return ""
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func newHTTP3Client(timeout time.Duration) (*http.Client, *http3.Transport) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		MaxVersion:         tls.VersionTLS13,
		ClientSessionCache: tls.NewLRUClientSessionCache(128),
		NextProtos:         []string{"h3", "h3-29", "h3-28", "h3-27"},
	}

	tr := &http3.Transport{
		TLSClientConfig: tlsCfg,
		QUICConfig: &quic.Config{
			KeepAlivePeriod: 10 * time.Second,
			MaxIdleTimeout:  5 * time.Minute,
			Allow0RTT:       true,
		},
	}

	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
	return client, tr
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Truncate(time.Millisecond))
	})
}

func (s *server) handleNodes(w http.ResponseWriter, r *http.Request) {
	resp := nodesResponse{
		BasePort: s.basePort,
		Count:    s.count,
		Nodes:    s.defaultNodes,
	}
	writeJSON(w, resp)
}

func (s *server) handleSummary(w http.ResponseWriter, r *http.Request) {
	req := summaryRequest{}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
	} else {
		req.Nodes = normalizeNodes(strings.Split(r.URL.Query().Get("nodes"), ","))
		req.IncludeBlock = parseBool(r.URL.Query().Get("include_block"))
		req.IncludeFrost = parseBool(r.URL.Query().Get("include_frost"))
	}

	nodes := normalizeNodes(req.Nodes)
	if len(nodes) == 0 {
		nodes = s.defaultNodes
	}

	start := time.Now()
	results, errs := s.collectSummary(r.Context(), nodes, req.IncludeBlock, req.IncludeFrost)
	resp := summaryResponse{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Nodes:       results,
		Errors:      errs,
		Selected:    nodes,
		ElapsedMs:   time.Since(start).Milliseconds(),
	}
	writeJSON(w, resp)
}

func parseBool(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false
	}
	return parsed
}

func (s *server) handleBlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req blockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, blockResponse{Error: "invalid request body"})
		return
	}
	if req.Node == "" {
		writeJSON(w, blockResponse{Error: "node is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	var block *pb.Block
	var fetchErr error

	if req.Hash != "" {
		// 按 hash 查询
		block, fetchErr = s.fetchBlockByID(ctx, req.Node, req.Hash)
	} else {
		// 按高度查询
		block, fetchErr = s.fetchBlockByHeight(ctx, req.Node, req.Height)
	}

	if fetchErr != nil {
		writeJSON(w, blockResponse{Error: fetchErr.Error()})
		return
	}

	info := convertBlockToInfo(block)
	writeJSON(w, blockResponse{Block: info})
}

func (s *server) handleTx(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req txRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, txResponse{Error: "invalid request body"})
		return
	}
	if req.Node == "" || req.TxID == "" {
		writeJSON(w, txResponse{Error: "node and tx_id are required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	anyTx, err := s.fetchTxByID(ctx, req.Node, req.TxID)
	if err != nil {
		writeJSON(w, txResponse{Error: err.Error()})
		return
	}

	info := convertAnyTxToInfo(anyTx)
	writeJSON(w, txResponse{Transaction: info})
}

func (s *server) fetchBlockByHeight(ctx context.Context, node string, height uint64) (*pb.Block, error) {
	var resp pb.GetBlockResponse
	req := &pb.GetBlockRequest{Height: height}
	if err := s.fetchProto(ctx, node, "/getblock", req, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	if resp.Block == nil {
		return nil, errors.New("block not found")
	}
	return resp.Block, nil
}

func (s *server) fetchBlockByID(ctx context.Context, node string, blockID string) (*pb.Block, error) {
	var resp pb.GetBlockResponse
	req := &pb.GetBlockByIDRequest{BlockId: blockID}
	if err := s.fetchProto(ctx, node, "/getblockbyid", req, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	if resp.Block == nil {
		return nil, errors.New("block not found")
	}
	return resp.Block, nil
}

func (s *server) fetchTxByID(ctx context.Context, node string, txID string) (*pb.AnyTx, error) {
	var resp pb.AnyTx
	req := &pb.GetData{TxId: txID}
	if err := s.fetchProto(ctx, node, "/getdata", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func convertBlockToInfo(block *pb.Block) *blockInfo {
	if block == nil {
		return nil
	}
	info := &blockInfo{
		Height:      block.Height,
		BlockHash:   block.BlockHash,
		PrevHash:    block.PrevBlockHash,
		TxsHash:     block.TxsHash,
		Miner:       block.Miner,
		TxCount:     len(block.Body),
		Accumulated: block.AccumulatedReward,
		Window:      block.Window,
		Txs:         make([]txSummary, 0, len(block.Body)),
	}
	for _, tx := range block.Body {
		summary := convertAnyTxToSummary(tx)
		info.Txs = append(info.Txs, summary)
	}
	return info
}

func convertAnyTxToSummary(tx *pb.AnyTx) txSummary {
	if tx == nil {
		return txSummary{}
	}
	summary := txSummary{
		TxID:   tx.GetTxId(),
		TxType: extractTxType(tx),
	}
	if base := tx.GetBase(); base != nil {
		summary.FromAddress = base.FromAddress
		summary.Status = base.Status.String()
	}
	summary.Summary = generateTxSummary(tx)
	return summary
}

func convertAnyTxToInfo(tx *pb.AnyTx) *txInfo {
	if tx == nil {
		return nil
	}
	info := &txInfo{
		TxID:    tx.GetTxId(),
		TxType:  extractTxType(tx),
		Details: make(map[string]interface{}),
	}
	if base := tx.GetBase(); base != nil {
		info.FromAddress = base.FromAddress
		info.Status = base.Status.String()
		info.ExecutedHeight = base.ExecutedHeight
		info.Fee = base.Fee
		info.Nonce = base.Nonce
	}
	// 填充详情
	fillTxDetails(tx, info)
	return info
}

func extractTxType(tx *pb.AnyTx) string {
	if tx == nil {
		return ""
	}
	switch tx.GetContent().(type) {
	case *pb.AnyTx_Transaction:
		return "Transaction"
	case *pb.AnyTx_IssueTokenTx:
		return "IssueToken"
	case *pb.AnyTx_FreezeTx:
		return "Freeze"
	case *pb.AnyTx_OrderTx:
		return "Order"
	case *pb.AnyTx_MinerTx:
		return "Miner"
	case *pb.AnyTx_WitnessStakeTx:
		return "WitnessStake"
	case *pb.AnyTx_WitnessRequestTx:
		return "WitnessRequest"
	case *pb.AnyTx_WitnessVoteTx:
		return "WitnessVote"
	case *pb.AnyTx_WitnessChallengeTx:
		return "WitnessChallenge"
	case *pb.AnyTx_ArbitrationVoteTx:
		return "ArbitrationVote"
	case *pb.AnyTx_WitnessClaimRewardTx:
		return "WitnessClaimReward"
	case *pb.AnyTx_FrostWithdrawRequestTx:
		return "FrostWithdrawRequest"
	case *pb.AnyTx_FrostWithdrawSignedTx:
		return "FrostWithdrawSigned"
	case *pb.AnyTx_FrostVaultDkgCommitTx:
		return "FrostVaultDkgCommit"
	case *pb.AnyTx_FrostVaultDkgShareTx:
		return "FrostVaultDkgShare"
	case *pb.AnyTx_FrostVaultDkgComplaintTx:
		return "FrostVaultDkgComplaint"
	case *pb.AnyTx_FrostVaultDkgRevealTx:
		return "FrostVaultDkgReveal"
	case *pb.AnyTx_FrostVaultDkgValidationSignedTx:
		return "FrostVaultDkgValidationSigned"
	case *pb.AnyTx_FrostVaultTransitionSignedTx:
		return "FrostVaultTransitionSigned"
	}
	return "Unknown"
}

func generateTxSummary(tx *pb.AnyTx) string {
	if tx == nil {
		return ""
	}
	switch c := tx.GetContent().(type) {
	case *pb.AnyTx_Transaction:
		t := c.Transaction
		return fmt.Sprintf("Transfer %s %s to %s", t.Amount, t.TokenAddress, t.To)
	case *pb.AnyTx_WitnessStakeTx:
		return fmt.Sprintf("Witness stake: %s", c.WitnessStakeTx.Op.String())
	case *pb.AnyTx_IssueTokenTx:
		return fmt.Sprintf("Issue token: %s", c.IssueTokenTx.TokenSymbol)
	case *pb.AnyTx_MinerTx:
		return fmt.Sprintf("Miner: %s", c.MinerTx.Op.String())
	}
	return extractTxType(tx)
}

func fillTxDetails(tx *pb.AnyTx, info *txInfo) {
	if tx == nil || info == nil {
		return
	}
	switch c := tx.GetContent().(type) {
	case *pb.AnyTx_Transaction:
		t := c.Transaction
		info.ToAddress = t.To
		info.Details["token_address"] = t.TokenAddress
		info.Details["amount"] = t.Amount
	case *pb.AnyTx_WitnessStakeTx:
		w := c.WitnessStakeTx
		info.Details["operation"] = w.Op.String()
		info.Details["amount"] = w.Amount
	case *pb.AnyTx_IssueTokenTx:
		i := c.IssueTokenTx
		info.Details["symbol"] = i.TokenSymbol
		info.Details["name"] = i.TokenName
		info.Details["total_supply"] = i.TotalSupply
		info.Details["can_mint"] = i.CanMint
	case *pb.AnyTx_MinerTx:
		m := c.MinerTx
		info.Details["operation"] = m.Op.String()
		info.Details["amount"] = m.Amount
	}
}

func (s *server) collectSummary(ctx context.Context, nodes []string, includeBlock, includeFrost bool) ([]nodeSummary, []string) {
	results := make([]nodeSummary, len(nodes))
	errs := make([]string, 0)
	var errsMu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for i, node := range nodes {
		i := i
		node := node
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			summary := nodeSummary{Address: node}
			start := time.Now()

			status, err := s.fetchStatus(ctx, node)
			if err != nil {
				summary.Error = err.Error()
				errsMu.Lock()
				errs = append(errs, fmt.Sprintf("%s status: %v", node, err))
				errsMu.Unlock()
			} else {
				summary.Status = status.Status
				summary.Info = status.Info
			}

			height, err := s.fetchHeight(ctx, node)
			if err != nil {
				if summary.Error != "" {
					summary.Error += "; "
				}
				summary.Error += err.Error()
				errsMu.Lock()
				errs = append(errs, fmt.Sprintf("%s height: %v", node, err))
				errsMu.Unlock()
			} else {
				summary.CurrentHeight = height.CurrentHeight
				summary.LastAcceptedHeight = height.LastAcceptedHeight
			}

			if includeFrost {
				metrics, err := s.fetchFrostMetrics(ctx, node)
				if err == nil && metrics != nil {
					summary.FrostMetrics = &frostMetrics{
						HeapAlloc:      metrics.HeapAlloc,
						HeapSys:        metrics.HeapSys,
						NumGoroutine:   metrics.NumGoroutine,
						FrostJobs:      metrics.FrostJobs,
						FrostWithdraws: metrics.FrostWithdraws,
					}
				}
			}

			if includeBlock && height != nil {
				block, err := s.fetchBlock(ctx, node, height.LastAcceptedHeight)
				if err == nil && block != nil {
					summary.Block = buildBlockSummary(block)
				}
			}

			summary.LatencyMs = time.Since(start).Milliseconds()
			results[i] = summary
		}()
	}

	wg.Wait()
	return results, errs
}

func (s *server) fetchStatus(ctx context.Context, node string) (*pb.StatusResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var resp pb.StatusResponse
	if err := s.fetchProto(ctx, node, "/status", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *server) fetchHeight(ctx context.Context, node string) (*pb.HeightResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var resp pb.HeightResponse
	if err := s.fetchProto(ctx, node, "/heightquery", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *server) fetchFrostMetrics(ctx context.Context, node string) (*pb.MetricsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var resp pb.MetricsResponse
	if err := s.fetchProto(ctx, node, "/frost/metrics", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *server) fetchBlock(ctx context.Context, node string, height uint64) (*pb.Block, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var resp pb.GetBlockResponse
	req := &pb.GetBlockRequest{Height: height}
	if err := s.fetchProto(ctx, node, "/getblock", req, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	if resp.Block == nil {
		return nil, errors.New("empty block")
	}
	return resp.Block, nil
}

func (s *server) fetchProto(ctx context.Context, node, path string, req proto.Message, resp proto.Message) error {
	url := fmt.Sprintf("https://%s%s", node, path)
	var body io.Reader
	method := http.MethodGet
	if req != nil {
		data, err := proto.Marshal(req)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
		method = http.MethodPost
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Accept", "application/x-protobuf")
	if req != nil {
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
	}

	httpResp, err := s.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		msg, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("status %d: %s", httpResp.StatusCode, strings.TrimSpace(string(msg)))
	}

	data, err := readResponseBody(httpResp)
	if err != nil {
		return err
	}

	return proto.Unmarshal(data, resp)
}

func readResponseBody(resp *http.Response) ([]byte, error) {
	reader := resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	return io.ReadAll(reader)
}

func buildBlockSummary(block *pb.Block) *blockSummary {
	if block == nil {
		return nil
	}
	typeCounts := make(map[string]int)
	for _, tx := range block.Body {
		typeCounts[txTypeName(tx)]++
	}

	return &blockSummary{
		Height:        block.Height,
		BlockHash:     block.BlockHash,
		PrevBlockHash: block.PrevBlockHash,
		TxsHash:       block.TxsHash,
		Miner:         block.Miner,
		TxCount:       len(block.Body),
		TxTypeCounts:  typeCounts,
		Accumulated:   block.AccumulatedReward,
		Window:        block.Window,
	}
}

func txTypeName(tx *pb.AnyTx) string {
	switch tx.GetContent().(type) {
	case *pb.AnyTx_IssueTokenTx:
		return "issue_token"
	case *pb.AnyTx_FreezeTx:
		return "freeze"
	case *pb.AnyTx_Transaction:
		return "transfer"
	case *pb.AnyTx_OrderTx:
		return "order"
	case *pb.AnyTx_MinerTx:
		return "miner"
	case *pb.AnyTx_WitnessStakeTx:
		return "witness_stake"
	case *pb.AnyTx_WitnessRequestTx:
		return "witness_request"
	case *pb.AnyTx_WitnessVoteTx:
		return "witness_vote"
	case *pb.AnyTx_WitnessChallengeTx:
		return "witness_challenge"
	case *pb.AnyTx_ArbitrationVoteTx:
		return "arbitration_vote"
	case *pb.AnyTx_WitnessClaimRewardTx:
		return "witness_claim_reward"
	case *pb.AnyTx_FrostWithdrawRequestTx:
		return "frost_withdraw_request"
	case *pb.AnyTx_FrostWithdrawSignedTx:
		return "frost_withdraw_signed"
	case *pb.AnyTx_FrostVaultDkgCommitTx:
		return "frost_dkg_commit"
	case *pb.AnyTx_FrostVaultDkgShareTx:
		return "frost_dkg_share"
	case *pb.AnyTx_FrostVaultDkgComplaintTx:
		return "frost_dkg_complaint"
	case *pb.AnyTx_FrostVaultDkgRevealTx:
		return "frost_dkg_reveal"
	case *pb.AnyTx_FrostVaultDkgValidationSignedTx:
		return "frost_dkg_validation"
	case *pb.AnyTx_FrostVaultTransitionSignedTx:
		return "frost_vault_transition"
	default:
		return "unknown"
	}
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func (s *server) handleNodeDetails(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		http.Error(w, "missing address", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	results, _ := s.collectSummary(ctx, []string{address}, true, true)
	if len(results) == 0 {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	details := nodeDetails{
		nodeSummary: results[0],
	}

	logs, err := s.fetchLogs(ctx, address)
	if err == nil {
		details.Logs = logs.Logs
	}

	recentBlocks, err := s.fetchRecentBlocks(ctx, address)
	if err == nil {
		details.RecentBlocks = recentBlocks.Blocks
	}

	writeJSON(w, details)
}

func (s *server) fetchRecentBlocks(ctx context.Context, node string) (*pb.GetRecentBlocksResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var resp pb.GetRecentBlocksResponse
	req := &pb.GetRecentBlocksRequest{Count: 50}
	if err := s.fetchProto(ctx, node, "/getrecentblocks", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *server) fetchLogs(ctx context.Context, node string) (*pb.LogsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var resp pb.LogsResponse
	req := &pb.LogsRequest{MaxLines: 1000} // 请求行数同步调大

	// 1. 优先尝试通过网络获取 (适用于分布式或活着的节点)
	err := s.fetchProto(ctx, node, "/logs", req, &resp)
	if err == nil {
		log.Printf("[fetchLogs] node=%s: fetched %d logs via network", node, len(resp.Logs))
		return &resp, nil
	}

	log.Printf("[fetchLogs] node=%s: network fetch failed: %v, trying local fallback", node, err)

	// 2. 如果网络获取失败 (节点可能停了)，在本地直接尝试从 logger 拿 (针对单进程仿真)
	logLines := logs.GetLogsForNode(node)
	log.Printf("[fetchLogs] node=%s: local fallback found %d logs (available nodes: %v)", node, len(logLines), logs.GetAllLoggedNodes())
	if len(logLines) > 0 {
		return &pb.LogsResponse{Logs: logLines}, nil
	}

	return nil, err
}
