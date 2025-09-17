package sender

import (
	"bytes"
	"dex/db"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// pullBatchTxMessage 封装了批量获取交易请求的payload及回调函数
type pullBatchTxMessage struct {
	requestData []byte
	// onSuccess 回调返回获取到的交易切片
	onSuccess func([]*db.AnyTx)
}

// 接口只需要传入shortHashes和回调函数
// 注意：该接口为异步接口，实际请求将通过全局发送队列提交
func BatchGetTxs(peerAddress string, shortHashes map[string]bool, onSuccess func([]*db.AnyTx)) {
	var result [][]byte
	for hexStr := range shortHashes {
		bytes, err := hex.DecodeString(hexStr)
		if err != nil {
			log.Printf("invalid hex string: %s, err: %v", hexStr, err)
			continue
		}
		result = append(result, bytes)
	}
	// 1. 构造请求消息
	reqMsg := &db.BatchGetShortTxRequest{
		ShortHashes: result,
	}
	data, err := proto.Marshal(reqMsg)
	if err != nil {
		fmt.Printf("failed to marshal BatchGetDataRequest: %v\n", err)
		return
	}

	// 2. 构造消息包装体
	msg := &pullBatchTxMessage{
		requestData: data,
		onSuccess:   onSuccess,
	}

	// 3. 根据传入的 peerAddress 获取对方 IP
	targetIP, err := Address_Ip(peerAddress)
	if err != nil {
		fmt.Printf("failed to get IP for peerAddress %s: %v\n", peerAddress, err)
		return
	}
	if targetIP == "" {
		fmt.Printf("peerAddress %s returned empty IP\n", peerAddress)
		return
	}

	// 4. 构造发送任务并提交到全局发送队列
	task := &SendTask{
		Target:     targetIP,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 1,
		SendFunc:   doSendBatchGetTxs,
	}
	GlobalQueue.Enqueue(task)
}

// doSendBatchGetTxs 执行HTTP/3 POST请求获取批量交易
func doSendBatchGetTxs(t *SendTask) error {
	msg, ok := t.Message.(*pullBatchTxMessage)
	if !ok {
		return fmt.Errorf("doSendBatchGetTxs: message is not *pullBatchTxMessage, got %T", t.Message)
	}

	// 构造请求URL，调用 /batchgetdata 接口
	url := fmt.Sprintf("https://%s/batchgetdata", t.Target)
	req, err := http.NewRequest("POST", url, bytes.NewReader(msg.requestData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	client := CreateHttp3Client()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("doSendBatchGetTxs: status=%d, body=%s", resp.StatusCode, string(respData))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var respMsg db.BatchGetShortTxResponse
	if err := proto.Unmarshal(respBytes, &respMsg); err != nil {
		return fmt.Errorf("doSendBatchGetTxs: unmarshal BatchGetDataResponse failed: %v", err)
	}

	// 调用回调函数传出结果
	if msg.onSuccess != nil {
		msg.onSuccess(respMsg.Transactions)
	}

	return nil
}
