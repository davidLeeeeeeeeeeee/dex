// 文件路径: sender/gossip_sender.go

package sender

import (
	"bytes"
	"dex/logs"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"dex/db"
)

// BroadcastToRandomMiners 将一份消息 msg 广播给任意 N 个随机矿工。
// 内部：先从DB随机挑选 N 个 -> 为每个矿工创建SendTask -> GlobalQueue.Enqueue。
func BroadcastToRandomMiners(msg []byte, count int) {

	miners, err := db.GetRandomMinersFast(count)
	if err != nil {
		logs.Error("[BroadcastToRandomMiners] GetRandomMiners error: %v", err)
		return
	}
	if len(miners) == 0 {
		logs.Error("[BroadcastToRandomMiners] no miners found (random).")
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
		GlobalQueue.Enqueue(task)
	}
	log.Printf("[BroadcastToRandomMiners] enqueued %d tasks to GlobalQueue.\n", len(miners))
}

// ------------------- 以下为核心发送逻辑 -------------------

// doSendToOnePeer 用于向单个目标节点发送消息。
// 这里示例：POST到对方的 /gossipAnyMsg，Content-Type=application/octet-stream。
func doSendToOnePeer(t *SendTask) error {
	// 先检查类型
	msgBytes, ok := t.Message.([]byte)
	if !ok {
		return fmt.Errorf("doSendToOnePeer: message is not []byte, got %T", t.Message)
	}
	if t.Target == "" {
		return errors.New("doSendToOnePeer: empty target ip")
	}

	// 1. 构造URL
	url := fmt.Sprintf("https://%s/gossipAnyMsg", t.Target)

	// 2. 构造HTTP/3请求
	req, err := http.NewRequest("POST", url, bytes.NewReader(msgBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	// 3. 用http3.Client执行请求
	client := CreateHttp3Client()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 4. 简单校验状态码
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("doSendToOnePeer: status=%d, target=%s, resp=%s",
			resp.StatusCode, t.Target, string(respBody))
	}

	logs.Trace("[Gossip] success to %s, messageLen=%d", t.Target, len(msgBytes))
	return nil
}
