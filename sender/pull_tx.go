package sender

import (
	"bytes"
	"dex/db"
	"dex/logs"
	"dex/txpool"
	"fmt"
	"github.com/golang/protobuf/proto"
	"io"
	"net"
	"net/http"
)

// pullTxMessage 用来包装“拉取请求的[]byte + 回调函数”
type pullTxMessage struct {
	requestData []byte
	onSuccess   func(*db.AnyTx)
}

// Address_Ip 用于判断输入是 IP/域名/地址，如果是合法 IP（可带端口），直接返回；否则到数据库查找。
// Address_Ip 用于判断输入是 IP/域名/地址，如果是合法 IP（可带端口），直接返回；否则到数据库查找。
func Address_Ip(peerAddr string) (string, error) {
	// 1. 尝试拆分成 IP:Port
	host, _, err := net.SplitHostPort(peerAddr)
	if err == nil {
		// 如果拆分成功，则 host=127.0.0.1, port=6123
		// 判断 host 是否是合法 IP
		if parsedIP := net.ParseIP(host); parsedIP != nil {
			// 如果是合法 IP，则相当于 "IP:Port" 形式，直接返回
			return peerAddr, nil
		}
		// 否则就继续往下走数据库查找逻辑
	} else {
		// 如果拆分失败，说明可能输入的就是纯 IP（无端口）或者域名等
		// 再判断一下是否是纯 IP 格式
		if parsedIP := net.ParseIP(peerAddr); parsedIP != nil {
			// 如果是合法 IP（纯 IP），直接返回
			return peerAddr, nil
		}
	}

	// 2. 如果上面都没能直接返回，则去数据库查找
	// 假设 peerAddr 是一个链上地址（如 bc1... 或 0x...）
	mgr, err := db.NewManager("")
	if err != nil {
		return "", fmt.Errorf("[Address_Ip] failed to get DB instance: %v", err)
	}

	// 3. 直接根据地址查询账户
	account, err := db.GetAccount(mgr, peerAddr)
	if err != nil {
		return "", fmt.Errorf("[Address_Ip] failed to get account for address %s: %v", peerAddr, err)
	}

	if account == nil {
		return "", fmt.Errorf("[Address_Ip] account not found for address: %s", peerAddr)
	}

	// 4. 检查账户是否有有效的 IP
	if account.Ip == "" {
		return "", fmt.Errorf("[Address_Ip] account %s has no IP address configured", peerAddr)
	}

	// 5. 返回找到的 IP
	return account.Ip, nil
}

// PullTx 对外暴露的函数：将 "获取指定 tx_id 的交易" 这件事放到发送队列里
func PullTx(peerAddr, txID string, onSuccess func(*db.AnyTx)) {
	// 1) 构造db.GetData + protobuf
	getDataMsg := &db.GetData{TxId: txID}
	data, err := proto.Marshal(getDataMsg)
	if err != nil {
		logs.Debug("[PullTx] marshal failed: %v", err)
		return
	}

	// 2) 找到对方ip
	ip, err := Address_Ip(peerAddr)
	if err != nil {
		logs.Debug("[PullTx] get address failed: %v", err)
		return
	}

	// 3) 用一个自定义 struct 把 (请求的 data, 回调) 都封装起来
	msg := &pullTxMessage{
		requestData: data,
		onSuccess:   onSuccess,
	}

	// 4) Enqueue
	task := &SendTask{
		Target:     ip,
		Message:    msg, // 注意这里不再是直接传 data
		RetryCount: 0,
		MaxRetries: 3,
		SendFunc:   doSendGetData,
	}
	GlobalQueue.Enqueue(task)
}

// doSendGetData 真正执行：POST /getdata，并解析返回的 AnyTx
func doSendGetData(t *SendTask) error {
	// 1) 解出pullTxMessage
	pm, ok := t.Message.(*pullTxMessage)
	if !ok {
		return fmt.Errorf("doSendGetData: t.Message is not *pullTxMessage, got %T", t.Message)
	}

	// 2) 构造HTTP请求
	url := fmt.Sprintf("https://%s/getdata", t.Target)
	req, err := http.NewRequest("POST", url, bytes.NewReader(pm.requestData))
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
		return fmt.Errorf("doSendGetData: status=%d, body=%s",
			resp.StatusCode, string(respData))
	}

	// 3) 解析 AnyTx
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var anyTx db.AnyTx
	if err := proto.Unmarshal(respBytes, &anyTx); err != nil {
		return fmt.Errorf("doSendGetData: unmarshal AnyTx fail: %v", err)
	}

	// 4) 默认逻辑：存TxPool
	txPool := txpool.GetInstance()
	txId := anyTx.GetTxId()
	if txId != "" {
		if err := txPool.StoreAnyTx(&anyTx); err != nil {
			logs.Debug("[doSendGetData] storeAnyTx fail: %v", err)
			// 这里看你要不要报错 or 忽略
		}
	}

	// 5) 如果有自定义回调，就调用
	if pm.onSuccess != nil {
		pm.onSuccess(&anyTx)
	}

	return nil
}
