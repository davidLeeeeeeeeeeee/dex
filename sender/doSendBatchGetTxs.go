package sender

import (
	"bytes"
	"dex/pb"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// pullBatchTxMessage 封装了批量获取交易请求的payload及回调函数
type pullBatchTxMessage struct {
	requestData []byte
	// onSuccess 回调返回获取到的交易切片
	onSuccess func([]*pb.AnyTx)
}

// 执行HTTP/3 POST请求获取批量交易
func doSendBatchGetTxs(t *SendTask, client *http.Client) error {
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

	var respMsg pb.BatchGetShortTxResponse
	if err := proto.Unmarshal(respBytes, &respMsg); err != nil {
		return fmt.Errorf("doSendBatchGetTxs: unmarshal BatchGetDataResponse failed: %v", err)
	}

	// 调用回调函数传出结果
	if msg.onSuccess != nil {
		msg.onSuccess(respMsg.Transactions)
	}

	return nil
}
