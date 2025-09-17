package sender

import (
	"bytes"
	"dex/db"
	"dex/logs"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

/* -------------------------------------------------------------------------- */
/* ---------------------------  PushQuery 发送  ----------------------------- */
/* -------------------------------------------------------------------------- */

type pushQueryMsg struct {
	requestData []byte
	onSuccess   func(*db.Chits) // 成功时收到对端 Chits 可选回调
}

// PushQuery 向 peerAddr 发送 PushQuery（本端已持有完整 container）
func PushQuery(peerAddr string, pq *db.PushQuery, onSuccess func(*db.Chits)) {
	logs.Info("PushQuery %s %s", peerAddr, pq.BlockId)
	data, err := proto.Marshal(pq)
	if err != nil {
		logs.Debug("[PushQuery] marshal fail: %v", err)
		return
	}
	ip, err := Address_Ip(peerAddr)
	if err != nil || ip == "" {
		logs.Debug("[PushQuery] resolve ip err: %v", err)
		return
	}
	msg := &pushQueryMsg{requestData: data, onSuccess: onSuccess}
	task := &SendTask{
		Target:     ip,
		Message:    msg,
		MaxRetries: 2,
		SendFunc:   doSendPushQuery,
	}
	GlobalQueue.Enqueue(task)
}

func doSendPushQuery(t *SendTask) error {
	pm, ok := t.Message.(*pushQueryMsg)
	if !ok {
		return fmt.Errorf("doSendPushQuery expect *pushQueryMsg, got %T", t.Message)
	}
	url := fmt.Sprintf("https://%s/pushquery", t.Target)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(pm.requestData))
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := CreateHttp3Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pushquery resp=%d, body=%s", resp.StatusCode, string(body))
	}

	// 预期对端立即返回 Chits
	respBytes, _ := io.ReadAll(resp.Body)
	var chits db.Chits
	if err := proto.Unmarshal(respBytes, &chits); err != nil {
		return err
	}
	if pm.onSuccess != nil {
		pm.onSuccess(&chits)
	}
	return nil
}

/* -------------------------------------------------------------------------- */
/* ---------------------------  PullQuery 发送  ----------------------------- */
/* -------------------------------------------------------------------------- */

type pullQueryMsg struct {
	requestData []byte
	onSuccess   func(*db.Chits)
}

func PullQuery(peerAddr string, pq *db.PullQuery, onSuccess func(*db.Chits)) {
	data, err := proto.Marshal(pq)
	if err != nil {
		logs.Debug("[PullQuery] marshal fail: %v", err)
		return
	}
	ip, err := Address_Ip(peerAddr)
	if err != nil || ip == "" {
		logs.Debug("[PullQuery] resolve ip err: %v", err)
		return
	}
	msg := &pullQueryMsg{requestData: data, onSuccess: onSuccess}
	task := &SendTask{
		Target:     ip,
		Message:    msg,
		MaxRetries: 2,
		SendFunc:   doSendPullQuery,
	}
	GlobalQueue.Enqueue(task)
}

func doSendPullQuery(t *SendTask) error {
	pm, ok := t.Message.(*pullQueryMsg)
	if !ok {
		return fmt.Errorf("doSendPullQuery expect *pullQueryMsg, got %T", t.Message)
	}
	url := fmt.Sprintf("https://%s/pullquery", t.Target)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(pm.requestData))
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := CreateHttp3Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pullquery resp=%d, body=%s", resp.StatusCode, string(body))
	}
	respBytes, _ := io.ReadAll(resp.Body)
	var chits db.Chits
	if err := proto.Unmarshal(respBytes, &chits); err != nil {
		return err
	}
	if pm.onSuccess != nil {
		pm.onSuccess(&chits)
	}
	return nil
}

// pullContainerMsg 用于封装拉取容器请求
type pullContainerMsg struct {
	requestData []byte
	onSuccess   func(*db.PushQuery) // 成功时回调，返回包含完整容器的 PushQuery
}

// PullContainer 主动向对端请求包含完整容器的 PushQuery 数据
// 用于当本地没有对应区块时，主动拉取
func PullContainer(peerAddr string, blockId string, height uint64, onSuccess func(*db.PushQuery)) {
	// 构造一个"请求容器"的消息（可以复用 PullQuery 结构）
	req := &db.PullQuery{
		BlockId:         blockId,
		RequestedHeight: height,
		Deadline:        0, // 可以设置超时
	}

	data, err := proto.Marshal(req)
	if err != nil {
		logs.Debug("[PullContainer] marshal fail: %v", err)
		return
	}

	ip, err := Address_Ip(peerAddr)
	if err != nil || ip == "" {
		logs.Debug("[PullContainer] resolve ip err: %v", err)
		return
	}

	msg := &pullContainerMsg{requestData: data, onSuccess: onSuccess}
	task := &SendTask{
		Target:     ip,
		Message:    msg,
		MaxRetries: 2,
		SendFunc:   doSendPullContainer,
	}
	GlobalQueue.Enqueue(task)
}

func doSendPullContainer(t *SendTask) error {
	pm, ok := t.Message.(*pullContainerMsg)
	if !ok {
		return fmt.Errorf("doSendPullContainer expect *pullContainerMsg, got %T", t.Message)
	}

	// 发送到一个新的端点 /pullcontainer
	url := fmt.Sprintf("https://%s/pullcontainer", t.Target)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(pm.requestData))
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := CreateHttp3Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pullcontainer resp=%d, body=%s", resp.StatusCode, string(body))
	}

	// 预期对端返回完整的 PushQuery（包含 container）
	respBytes, _ := io.ReadAll(resp.Body)
	var pushQuery db.PushQuery
	if err := proto.Unmarshal(respBytes, &pushQuery); err != nil {
		return err
	}

	if pm.onSuccess != nil {
		pm.onSuccess(&pushQuery)
	}
	return nil
}
