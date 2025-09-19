// 文件: sender/pull_block.go
package sender

import (
	"bytes"
	"compress/gzip"
	"dex/db"
	"dex/logs"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
)

// pullBlockMessage 用来包装：要请求的 height + 回调函数
type pullBlockMessage struct {
	requestData []byte
	onSuccess   func(*db.Block)
}

// PullBlock 对外暴露的函数：发起对 peerAddr 的 /getblock 请求，想获取指定 height 的区块
// onSuccess 回调可在请求成功后拿到区块做后续处理。
func PullBlock(targetAddress string, height uint64, onSuccess func(*db.Block)) {
	mgr, err := db.NewManager("")
	if err != nil {
		logs.Debug("[PullBlock] GetInstance error: %v", err)
		return
	}
	// 根据链上地址获取账户信息，从中获得IP
	account, err := db.GetAccount(mgr, targetAddress)
	if err != nil || account == nil {
		logs.Debug("[PullBlock] account not found for address %s", targetAddress)
		return
	}
	if account.Ip == "" {
		logs.Debug("[PullBlock] IP is empty for address %s", targetAddress)
		return
	}

	// 构造 GetBlockRequest 消息
	req := &db.GetBlockRequest{Height: height}
	data, err := proto.Marshal(req)
	if err != nil {
		logs.Debug("[PullBlock] marshal GetBlockRequest error: %v", err)
		return
	}

	// 构造包装消息
	msg := &pullBlockMessage{
		requestData: data,
		onSuccess:   onSuccess,
	}

	// 构造 SendTask，将目标设为从数据库查到的 IP
	task := &SendTask{
		Target:     account.Ip,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 1,
		SendFunc:   doSendGetBlock,
	}
	GlobalQueue.Enqueue(task)

	logs.Debug("Pull block at height=%d from %s ", height, targetAddress)
}

// doSendGetBlock 真正执行 HTTP/3 POST /getblock 并解析返回
func doSendGetBlock(t *SendTask) error {
	pm, ok := t.Message.(*pullBlockMessage)
	if !ok {
		return fmt.Errorf("doSendGetBlock: t.Message is not *pullBlockMessage")
	}

	url := fmt.Sprintf("https://%s/getblock", t.Target)
	client := CreateHttp3Client()

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
		return fmt.Errorf("doSendGetBlock: status=%d, body=%s",
			resp.StatusCode, string(respData))
	}

	// 如果服务端使用 gzip 压缩，则需要解压
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("doSendGetBlock: cannot create gzip reader: %v", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	// 读取响应体并反序列化
	respBytes, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("doSendGetBlock: read body error: %v", err)
	}

	var blockResp db.GetBlockResponse
	if err := proto.Unmarshal(respBytes, &blockResp); err != nil {
		return fmt.Errorf("doSendGetBlock: unmarshal GetBlockResponse err=%v", err)
	}
	if blockResp.Error != "" {
		return fmt.Errorf("doSendGetBlock: remote error: %s", blockResp.Error)
	}

	// 调用 onSuccess 回调
	if pm.onSuccess != nil && blockResp.Block != nil {
		pm.onSuccess(blockResp.Block)
	}

	return nil
}
