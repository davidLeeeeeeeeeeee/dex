package syncer

import (
	"context"
	"crypto/tls"
	"dex/cmd/explorer/indexdb"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// Syncer 区块同步器
type Syncer struct {
	indexDB    *indexdb.IndexDB
	node       string        // 同步的目标节点
	interval   time.Duration // 同步间隔
	httpClient *http.Client

	mu       sync.RWMutex
	running  bool
	stopCh   chan struct{}
	doneCh   chan struct{}
	lastSync time.Time
	status   string
}

// New 创建新的同步器
func New(indexDB *indexdb.IndexDB, node string, interval time.Duration) *Syncer {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	// 创建 HTTP/3 客户端 - 节点使用 QUIC 协议
	transport := &http3.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // 自签名证书
			NextProtos:         []string{"h3"},
		},
		QUICConfig: &quic.Config{
			MaxIdleTimeout:  30 * time.Second,
			KeepAlivePeriod: 10 * time.Second,
		},
	}

	return &Syncer{
		indexDB:  indexDB,
		node:     node,
		interval: interval,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
		status: "stopped",
	}
}

// Start 启动同步器
func (s *Syncer) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("syncer already running")
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.status = "running"
	s.mu.Unlock()

	// 保存同步节点
	s.indexDB.SetSyncNode(s.node)

	go s.syncLoop(ctx)
	return nil
}

// Stop 停止同步器
func (s *Syncer) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	s.mu.Unlock()
	<-s.doneCh
}

// SetNode 设置同步节点
func (s *Syncer) SetNode(node string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.node = node
	s.indexDB.SetSyncNode(node)
}

// GetNode 获取当前同步节点
func (s *Syncer) GetNode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.node
}

// GetStatus 获取同步状态
func (s *Syncer) GetStatus() (string, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status, s.lastSync
}

// syncLoop 同步循环
func (s *Syncer) syncLoop(ctx context.Context) {
	defer close(s.doneCh)

	// 先执行一次完整同步
	s.syncOnce(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.syncOnce(ctx)
		}
	}
}

// syncOnce 执行一次同步
func (s *Syncer) syncOnce(ctx context.Context) {
	s.mu.Lock()
	node := s.node
	s.status = "syncing"
	s.mu.Unlock()

	if node == "" {
		s.setStatus("no node configured")
		return
	}

	// 获取当前同步高度
	syncHeight, err := s.indexDB.GetSyncHeight()
	if err != nil {
		s.setStatus(fmt.Sprintf("error getting sync height: %v", err))
		return
	}

	// 获取节点最新高度
	latestHeight, err := s.getLatestHeight(ctx, node)
	if err != nil {
		s.setStatus(fmt.Sprintf("error getting latest height: %v", err))
		return
	}

	// 同步缺失的区块
	synced := 0
	for height := syncHeight + 1; height <= latestHeight; height++ {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		if err := s.syncBlock(ctx, node, height); err != nil {
			s.setStatus(fmt.Sprintf("error syncing block %d: %v", height, err))
			break
		}
		synced++
	}

	s.mu.Lock()
	s.lastSync = time.Now()
	s.status = fmt.Sprintf("synced to %d (fetched %d blocks)", latestHeight, synced)
	s.mu.Unlock()
}
