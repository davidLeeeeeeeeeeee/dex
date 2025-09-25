package sender

import (
	"bytes"
	"crypto/tls"
	"dex/db"
	"dex/logs"
	"dex/txpool"
	"encoding/hex"
	"fmt"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"io"
	"net"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"
)

// SenderManager 管理所有发送操作及其依赖
type SenderManager struct {
	dbManager  *db.Manager
	txPool     *txpool.TxPool
	address    string     // 本节点地址
	sendQueue  *SendQueue // 持有SendQueue实例
	httpClient *http.Client
}

// NewSenderManager 创建新的发送管理器
func NewSenderManager(dbMgr *db.Manager, address string, pool *txpool.TxPool) *SenderManager {
	// 创建 HTTP/3 客户端
	httpClient := createHttp3Client()

	// 创建SendQueue实例，传入 httpClient
	queue := NewSendQueue(10, 10000, httpClient)

	return &SenderManager{
		dbManager:  dbMgr,
		txPool:     pool,
		address:    address,
		sendQueue:  queue,
		httpClient: httpClient,
	}
}

// createHttp3Client 创建非单例的 HTTP/3 客户端
func createHttp3Client() *http.Client {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		MaxVersion:         tls.VersionTLS13,
		ClientSessionCache: tls.NewLRUClientSessionCache(128),
		// 添加ALPN协议支持
		NextProtos: []string{"h3", "h3-29", "h3-28", "h3-27"},
	}

	tr := &http3.Transport{
		TLSClientConfig: tlsCfg,
		QUICConfig: &quic.Config{
			KeepAlivePeriod: 10 * time.Second,
			MaxIdleTimeout:  5 * time.Minute,
			// 可选：添加0-RTT支持
			Allow0RTT: true,
		},
	}

	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
	}
}

// BroadcastTx 广播交易
func (sm *SenderManager) BroadcastTx(tx *db.AnyTx) {
	data, err := proto.Marshal(tx)
	if err != nil {
		logs.Verbose("BroadcastTx: proto marshal failed: %v", err)
		return
	}

	peers, err := sm.getRandomMiners(3)
	if err != nil {
		logs.Error("BroadcastTx: failed to get miners: %v", err)
		return
	}

	for _, account := range peers {
		task := &SendTask{
			Target:     account.Ip,
			Message:    data,
			RetryCount: 0,
			MaxRetries: 1,
			SendFunc:   doSendTx,
		}
		sm.sendQueue.Enqueue(task) // 使用实例的队列
	}
}

// SendTxToAllPeers 发送交易给所有节点
func (sm *SenderManager) SendTxToAllPeers(tx *db.AnyTx) {
	data, err := proto.Marshal(tx)
	if err != nil {
		logs.Error("SendTxToAllPeers: marshal failed: %v", err)
		return
	}

	accounts, err := sm.getRandomMiners(20)
	if err != nil {
		logs.Error("SendTxToAllPeers: failed to get miners: %v", err)
		return
	}

	for _, acc := range accounts {
		task := &SendTask{
			Target:     acc.Ip,
			Message:    data,
			RetryCount: 0,
			MaxRetries: 3,
			SendFunc:   doSendTx,
		}
		sm.sendQueue.Enqueue(task)
	}
}

// PullTx 拉取指定交易
func (sm *SenderManager) PullTx(peerAddr, txID string, onSuccess func(*db.AnyTx)) {
	getDataMsg := &db.GetData{TxId: txID}
	data, err := proto.Marshal(getDataMsg)
	if err != nil {
		logs.Debug("[PullTx] marshal failed: %v", err)
		return
	}

	ip, err := sm.addressToIp(peerAddr)
	if err != nil {
		logs.Debug("[PullTx] get address failed: %v", err)
		return
	}

	msg := &pullTxMessage{
		requestData: data,
		onSuccess:   onSuccess,
		txPool:      sm.txPool,
	}

	task := &SendTask{
		Target:     ip,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 3,
		SendFunc:   doSendGetDataWithManager,
	}
	sm.sendQueue.Enqueue(task)
}

// PullBlock 拉取指定高度的区块
func (sm *SenderManager) PullBlock(targetAddress string, height uint64, onSuccess func(*db.Block)) {
	account, err := db.GetAccount(sm.dbManager, targetAddress)
	if err != nil || account == nil {
		logs.Debug("[PullBlock] account not found for address %s", targetAddress)
		return
	}

	if account.Ip == "" {
		logs.Debug("[PullBlock] IP is empty for address %s", targetAddress)
		return
	}

	req := &db.GetBlockRequest{Height: height}
	data, err := proto.Marshal(req)
	if err != nil {
		logs.Debug("[PullBlock] marshal GetBlockRequest error: %v", err)
		return
	}

	msg := &pullBlockMessage{
		requestData: data,
		onSuccess:   onSuccess,
	}

	task := &SendTask{
		Target:     account.Ip,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 1,
		SendFunc:   doSendGetBlock,
	}
	sm.sendQueue.Enqueue(task)
}

// BatchGetTxs 批量获取交易
func (sm *SenderManager) BatchGetTxs(peerAddress string, shortHashes map[string]bool, onSuccess func([]*db.AnyTx)) {
	var result [][]byte
	for hexStr := range shortHashes {
		bytes, err := hex.DecodeString(hexStr)
		if err != nil {
			continue
		}
		result = append(result, bytes)
	}

	reqMsg := &db.BatchGetShortTxRequest{
		ShortHashes: result,
	}
	data, err := proto.Marshal(reqMsg)
	if err != nil {
		fmt.Printf("failed to marshal BatchGetDataRequest: %v\n", err)
		return
	}

	ip, err := sm.addressToIp(peerAddress)
	if err != nil {
		fmt.Printf("failed to get IP for peerAddress %s: %v\n", peerAddress, err)
		return
	}

	msg := &pullBatchTxMessage{
		requestData: data,
		onSuccess:   onSuccess,
	}

	task := &SendTask{
		Target:     ip,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 1,
		SendFunc:   doSendBatchGetTxs,
	}
	sm.sendQueue.Enqueue(task)
}

// 广播消息给随机矿工
func (sm *SenderManager) BroadcastToRandomMiners(msg []byte, count int) {
	miners, err := sm.getRandomMiners(count)
	if err != nil {
		logs.Error("[BroadcastToRandomMiners] GetRandomMiners error: %v", err)
		return
	}

	for _, m := range miners {
		if m.Ip == "" {
			continue
		}
		task := &SendTask{
			Target:     m.Ip,
			Message:    msg,
			RetryCount: 0,
			MaxRetries: 3,
			SendFunc:   doSendToOnePeer,
		}
		sm.sendQueue.Enqueue(task)
	}
}

// 发送PushQuery（Snowman共识）
func (sm *SenderManager) PushQuery(peerAddr string, pq *db.PushQuery) {
	// 确保 PushQuery 包含发送方地址
	pq.Address = sm.address // 使用节点自己的地址
	data, err := proto.Marshal(pq)
	if err != nil {
		logs.Debug("[PushQuery] marshal fail: %v", err)
		return
	}

	ip, err := sm.addressToIp(peerAddr)
	if err != nil || ip == "" {
		logs.Debug("[PushQuery] resolve ip err: %v", err)
		return
	}

	msg := &pushQueryMsg{requestData: data}
	task := &SendTask{
		Target:     ip,
		Message:    msg,
		MaxRetries: 2,
		SendFunc:   doSendPushQuery,
	}
	sm.sendQueue.Enqueue(task)
}

// PullQuery 发送PullQuery（Snowman共识）
func (sm *SenderManager) PullQuery(peerAddr string, pq *db.PullQuery) {
	data, err := proto.Marshal(pq)
	if err != nil {
		logs.Debug("[PullQuery] marshal fail: %v", err)
		return
	}

	ip, err := sm.addressToIp(peerAddr)
	if err != nil || ip == "" {
		logs.Debug("[PullQuery] resolve ip err: %v", err)
		return
	}

	msg := &pullQueryMsg{requestData: data}
	task := &SendTask{
		Target:     ip,
		Message:    msg,
		MaxRetries: 2,
		SendFunc:   doSendPullQuery,
	}
	sm.sendQueue.Enqueue(task)
}

// 辅助方法

// getRandomMiners 获取随机矿工列表
func (sm *SenderManager) getRandomMiners(count int) ([]*db.Account, error) {
	// 使用注入的dbManager，而不是创建新的
	return db.GetRandomMinersFast(sm.dbManager, count)
}

// 将地址转换为IP（确保包含端口）
func (sm *SenderManager) addressToIp(peerAddr string) (string, error) {
	// 检查是否已经是IP:Port格式
	if host, _, err := net.SplitHostPort(peerAddr); err == nil {
		if parsedIP := net.ParseIP(host); parsedIP != nil {
			return peerAddr, nil // 已经是IP:Port格式
		}
	}

	// 检查是否是纯IP（需要查询默认端口）
	if parsedIP := net.ParseIP(peerAddr); parsedIP != nil {
		// 这是纯IP，需要从数据库查询端口或使用默认端口
		return peerAddr, nil
	}

	// 从数据库查询地址对应的IP（应该包含端口）
	account, err := db.GetAccount(sm.dbManager, peerAddr)
	if err != nil {
		return "", fmt.Errorf("failed to get account for address %s: %v", peerAddr, err)
	}

	if account == nil || account.Ip == "" {
		return "", fmt.Errorf("no IP configured for address %s", peerAddr)
	}

	// account.Ip 应该已包含端口，如 "127.0.0.1:6000"
	return account.Ip, nil
}

// 新的发送函数，使用txPool引用
func doSendGetDataWithManager(t *SendTask, client *http.Client) error {
	pm, ok := t.Message.(*pullTxMessage)
	if !ok {
		return fmt.Errorf("doSendGetData: t.Message is not *pullTxMessage")
	}

	url := fmt.Sprintf("https://%s/getdata", t.Target)
	req, err := http.NewRequest("POST", url, bytes.NewReader(pm.requestData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("doSendGetData: status=%d, body=%s", resp.StatusCode, string(respData))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var anyTx db.AnyTx
	if err := proto.Unmarshal(respBytes, &anyTx); err != nil {
		return fmt.Errorf("doSendGetData: unmarshal AnyTx fail: %v", err)
	}

	// 使用传递的txPool引用
	if pm.txPool != nil {
		txId := anyTx.GetTxId()
		if txId != "" {
			if err := pm.txPool.StoreAnyTx(&anyTx); err != nil {
				logs.Debug("[doSendGetData] storeAnyTx fail: %v", err)
			}
		}
	}

	if pm.onSuccess != nil {
		pm.onSuccess(&anyTx)
	}

	return nil
}

// 发送Chits消息（Snowman共识响应）
func (sm *SenderManager) SendChits(targetAddress string, chits *db.Chits) error {
	chits.Address = sm.address // 添加发送方地址
	data, err := proto.Marshal(chits)
	if err != nil {
		logs.Debug("[SendChits] marshal failed: %v", err)
		return err
	}

	ip, err := sm.addressToIp(targetAddress)
	if err != nil {
		logs.Debug("[SendChits] failed to get IP for address %s: %v", targetAddress, err)
		return err
	}

	msg := &chitsMessage{
		requestData: data,
	}

	task := &SendTask{
		Target:     ip,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 2,
		SendFunc:   doSendChits,
	}
	sm.sendQueue.Enqueue(task)
	return nil
}

// SendBlock 发送完整区块数据
func (sm *SenderManager) SendBlock(targetAddress string, block *db.Block) error {
	data, err := proto.Marshal(block)
	if err != nil {
		logs.Debug("[SendBlock] marshal failed: %v", err)
		return err
	}

	ip, err := sm.addressToIp(targetAddress)
	if err != nil {
		logs.Debug("[SendBlock] failed to get IP for address %s: %v", targetAddress, err)
		return err
	}

	msg := &blockMessage{
		requestData: data,
	}

	task := &SendTask{
		Target:     ip,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 2,
		SendFunc:   doSendBlock,
	}
	sm.sendQueue.Enqueue(task)
	return nil
}

// SendHeightQuery 发送高度查询请求
func (sm *SenderManager) SendHeightQuery(targetAddress string, onSuccess func(*db.HeightResponse)) error {
	// 构造空的高度查询请求
	req := &db.StatusRequest{} // 使用StatusRequest或创建专门的HeightQueryRequest
	data, err := proto.Marshal(req)
	if err != nil {
		logs.Debug("[SendHeightQuery] marshal failed: %v", err)
		return err
	}

	ip, err := sm.addressToIp(targetAddress)
	if err != nil {
		logs.Debug("[SendHeightQuery] failed to get IP for address %s: %v", targetAddress, err)
		return err
	}

	msg := &heightQueryMessage{
		requestData: data,
		onSuccess:   onSuccess,
	}

	task := &SendTask{
		Target:     ip,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 2,
		SendFunc:   doSendHeightQuery,
	}
	sm.sendQueue.Enqueue(task)
	return nil
}

// SendSyncRequest 发送同步请求
func (sm *SenderManager) SendSyncRequest(targetAddress string, fromHeight, toHeight uint64, onSuccess func([]*db.Block)) error {
	req := &db.GetBlockRequest{
		Height: fromHeight, // 可以扩展为支持范围查询
	}
	data, err := proto.Marshal(req)
	if err != nil {
		logs.Debug("[SendSyncRequest] marshal failed: %v", err)
		return err
	}

	ip, err := sm.addressToIp(targetAddress)
	if err != nil {
		logs.Debug("[SendSyncRequest] failed to get IP for address %s: %v", targetAddress, err)
		return err
	}

	msg := &syncRequestMessage{
		requestData: data,
		fromHeight:  fromHeight,
		toHeight:    toHeight,
		onSuccess:   onSuccess,
	}

	task := &SendTask{
		Target:     ip,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 2,
		SendFunc:   doSendSyncRequest,
	}
	sm.sendQueue.Enqueue(task)
	return nil
}

// Stop 停止SenderManager（包括其队列）
func (sm *SenderManager) Stop() {
	if sm.sendQueue != nil {
		sm.sendQueue.Stop()
	}
}
