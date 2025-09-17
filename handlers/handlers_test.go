package handlers

import (
	"bufio"
	"dex/logs"
	"dex/utils"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
)

// TxData 用于与前面保持一致的结构（请确保和生成的一致）
type TxData struct {
	Message      string `json:"message"`
	PublicKeyPEM string `json:"public_key_pem"`
	SignatureHex string `json:"signature_hex"`
}

func TestVerifyTxData(t *testing.T) {
	t.Parallel()

	inputFile := "tx_data.jsonl"
	data, err := loadTxData(inputFile)
	if err != nil {
		t.Fatalf("failed to load tx data: %v", err)
	}

	// 开始计时
	start := time.Now()

	// 使用多线程验证
	numWorkers := 16 // 可根据CPU核数调整
	verifyCh := make(chan TxData, len(data))
	resultsCh := make(chan bool, len(data))

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			for tx := range verifyCh {
				pubKey, err := utils.DecodePublicKey(tx.PublicKeyPEM)
				if err != nil {
					resultsCh <- false
					continue
				}
				valid := utils.VerifySignature(pubKey, tx.Message, tx.SignatureHex)
				resultsCh <- valid
			}
		}()
	}

	// 将数据写入 verifyCh
	for _, d := range data {
		verifyCh <- d
	}
	close(verifyCh)

	// 等待所有协程完成
	wg.Wait()
	close(resultsCh)

	// 检查所有结果
	count := 0
	for res := range resultsCh {
		if !res {
			t.Fatalf("Found invalid signature!")
		}
		count++
	}

	if count != len(data) {
		t.Fatalf("Some transactions were not verified. Expected %d, got %d", len(data), count)
	}

	elapsed := time.Since(start)
	logs.Debug("Verified %d transactions in %s\n", count, elapsed)
}

func loadTxData(filename string) ([]TxData, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var result []TxData
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var d TxData
		line := scanner.Bytes()
		if err := json.Unmarshal(line, &d); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
