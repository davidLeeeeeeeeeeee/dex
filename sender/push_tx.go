package sender

import (
	"bytes"
	"dex/logs"
	"fmt"
	"io"
	"net/http"

	"dex/db"
	"google.golang.org/protobuf/proto"
)

func BroadcastTx(tx *db.AnyTx) {
	data, err := proto.Marshal(tx)
	if err != nil {
		logs.Verbose("BroadcastInv: proto marshal failed: %v", err)
		return
	}

	// 假设我们已有一个全局的节点列表（例如从 network.NewNetwork 得到）
	peers, _ := db.GetRandomMinersFast(3)
	for _, account := range peers {
		// 创建一个发送任务，这里采用与其他 sender 类似的队列机制
		task := &SendTask{
			Target:     account.Ip,
			Message:    data,
			RetryCount: 0,
			MaxRetries: 1,
			SendFunc:   doSendTx,
		}
		GlobalQueue.Enqueue(task)
	}
}
func SendTxToAllPeers(tx *db.AnyTx) {
	//logs.Info("SendTxToAllPeers %s", tx.GetTxId())
	data, err := proto.Marshal(tx)
	// 随机选出miner广播
	accounts, err := db.GetRandomMinersFast(20)
	if err != nil {
		logs.Error("[SendTxToAllPeers] failed to get GetRandomMinersFast: %v", err)
		return
	}

	// 遍历这些账户，将它们的地址作为 peer 依次投递发送任务
	for _, acc := range accounts {
		// 创建一个发送任务，这里采用与其他 sender 类似的队列机制
		task := &SendTask{
			Target:     acc.Ip,
			Message:    data,
			RetryCount: 0,
			MaxRetries: 3,
			SendFunc:   doSendTx,
		}
		GlobalQueue.Enqueue(task)
	}
}

// doSendTx 实现对单个节点通过 HTTP/3 发送 inv 消息
func doSendTx(t *SendTask) error {
	// 构造 URL，假定服务端对 inv 消息的处理接口为 /inv
	//logs.Trace("[inv] Sent inv for tx_id=%s to %s", extractTxIDFromInvData(t.Message.([]byte)), t.Target)
	url := fmt.Sprintf("https://%s/tx", t.Target)
	req, err := http.NewRequest("POST", url, bytes.NewReader(t.Message.([]byte)))
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
		return fmt.Errorf("doSendTx: status %d, body %s", resp.StatusCode, string(respData))
	}

	return nil
}
