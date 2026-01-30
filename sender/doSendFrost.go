// sender/doSendFrost.go
// Frost P2P 消息发送功能

package sender

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"dex/logs"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// frostMessage Frost 消息封装
type frostMessage struct {
	envelope *pb.FrostEnvelope
}

// doSendFrost 发送 Frost 消息
func doSendFrost(t *SendTask, client *http.Client) error {
	fm, ok := t.Message.(*frostMessage)
	if !ok {
		return fmt.Errorf("doSendFrost: message is not *frostMessage")
	}

	data, err := proto.Marshal(fm.envelope)
	if err != nil {
		return fmt.Errorf("doSendFrost: marshal failed: %w", err)
	}

	url := fmt.Sprintf("https://%s/frostmsg", t.Target)
	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("doSendFrost: create request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("doSendFrost: send failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("doSendFrost: status=%d, body=%s", resp.StatusCode, string(respData))
	}

	return nil
}

// SendFrost 发送 Frost 消息到指定节点
func (sm *SenderManager) SendFrost(targetIP string, envelope *pb.FrostEnvelope) error {
	if targetIP == "" {
		return fmt.Errorf("SendFrost: empty target IP")
	}

	msg := &frostMessage{envelope: envelope}

	task := &SendTask{
		Target:     targetIP,
		Message:    msg,
		RetryCount: 0,
		MaxRetries: 2,
		SendFunc:   doSendFrost,
		Priority:   PriorityControl, // Frost 消息使用控制面优先级
	}

	sm.SendQueue.Enqueue(task)
	logs.Debug("[SendFrost] enqueued to %s, kind=%v, job=%s", targetIP, envelope.Kind, envelope.JobId)
	return nil
}

// SendFrostToAddress 发送 Frost 消息到指定地址（通过地址查询IP）
func (sm *SenderManager) SendFrostToAddress(targetAddress string, envelope *pb.FrostEnvelope) error {
	ip, err := sm.AddressToIP(targetAddress)
	if err != nil {
		return fmt.Errorf("SendFrostToAddress: failed to get IP for %s: %w", targetAddress, err)
	}

	return sm.SendFrost(ip, envelope)
}

// BroadcastFrost 广播 Frost 消息给多个节点
func (sm *SenderManager) BroadcastFrost(targetIPs []string, envelope *pb.FrostEnvelope) {
	for _, ip := range targetIPs {
		if err := sm.SendFrost(ip, envelope); err != nil {
			logs.Debug("[BroadcastFrost] failed to send to %s: %v", ip, err)
		}
	}
}
