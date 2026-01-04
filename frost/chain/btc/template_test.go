package btc

import (
	"bytes"
	"testing"

	"dex/frost/chain"
)

// 测试用的固定 txid（32 字节 hex）
const (
	txid1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	txid2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	txid3 = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// TestTemplateHash_DifferentInputOrder 测试不同输入顺序产生相同 hash
func TestTemplateHash_DifferentInputOrder(t *testing.T) {
	// totalIn = 80000, totalOut = 30000, fee = 1000, change = 49000
	// 构造参数 - 顺序 1
	params1 := chain.WithdrawTemplateParams{
		Chain:       "btc",
		Asset:       "native",
		VaultID:     1,
		KeyEpoch:    100,
		WithdrawIDs: []string{"w1", "w2"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr1", Amount: 10000},
			{WithdrawID: "w2", To: "bc1qaddr2", Amount: 20000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid1, Vout: 0, Amount: 50000, ConfirmHeight: 100},
			{TxID: txid2, Vout: 1, Amount: 30000, ConfirmHeight: 101},
		},
		ChangeAddress: "bc1qchange",
		Fee:           1000,
		ChangeAmount:  49000,
	}

	// 构造参数 - 顺序 2（inputs 顺序颠倒）
	params2 := chain.WithdrawTemplateParams{
		Chain:       "btc",
		Asset:       "native",
		VaultID:     1,
		KeyEpoch:    100,
		WithdrawIDs: []string{"w1", "w2"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr1", Amount: 10000},
			{WithdrawID: "w2", To: "bc1qaddr2", Amount: 20000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid2, Vout: 1, Amount: 30000, ConfirmHeight: 101}, // 顺序颠倒
			{TxID: txid1, Vout: 0, Amount: 50000, ConfirmHeight: 100},
		},
		ChangeAddress: "bc1qchange",
		Fee:           1000,
		ChangeAmount:  49000,
	}

	// 创建模板
	template1, err := NewBTCTemplate(params1)
	if err != nil {
		t.Fatalf("NewBTCTemplate params1 failed: %v", err)
	}

	template2, err := NewBTCTemplate(params2)
	if err != nil {
		t.Fatalf("NewBTCTemplate params2 failed: %v", err)
	}

	// 计算 hash
	hash1 := template1.TemplateHash()
	hash2 := template2.TemplateHash()

	// 验证 hash 相同
	if !bytes.Equal(hash1, hash2) {
		t.Errorf("TemplateHash mismatch for different input order:\nhash1: %x\nhash2: %x", hash1, hash2)
	}

	// 验证输入已排序
	if template1.Inputs[0].TxID != txid1 {
		t.Errorf("Expected first input TxID=%s, got %s", txid1, template1.Inputs[0].TxID)
	}
}

// TestTemplateHash_MultipleInputs 测试多输入场景
func TestTemplateHash_MultipleInputs(t *testing.T) {
	// 三个输入，乱序
	// totalIn = 150000, totalOut = 100000, fee = 2000, change = 48000
	params := chain.WithdrawTemplateParams{
		Chain:       "btc",
		Asset:       "native",
		VaultID:     2,
		KeyEpoch:    200,
		WithdrawIDs: []string{"w1"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qbigwithdraw", Amount: 100000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid3, Vout: 0, Amount: 50000, ConfirmHeight: 100},
			{TxID: txid1, Vout: 2, Amount: 40000, ConfirmHeight: 98},
			{TxID: txid2, Vout: 1, Amount: 60000, ConfirmHeight: 99},
		},
		ChangeAddress: "bc1qchange",
		Fee:           2000,
		ChangeAmount:  48000,
	}

	template, err := NewBTCTemplate(params)
	if err != nil {
		t.Fatalf("NewBTCTemplate failed: %v", err)
	}

	// 验证输入排序（按 txid 字典序）
	expectedOrder := []string{txid1, txid2, txid3}
	for i, expected := range expectedOrder {
		if template.Inputs[i].TxID != expected {
			t.Errorf("Input[%d]: expected TxID=%s, got %s", i, expected, template.Inputs[i].TxID)
		}
	}

	// 验证 hash 可重复计算
	hash1 := template.TemplateHash()
	hash2 := template.TemplateHash()
	if !bytes.Equal(hash1, hash2) {
		t.Error("TemplateHash should be deterministic")
	}
}

// TestTemplateHash_SameTxidDifferentVout 测试相同 txid 不同 vout 的排序
func TestTemplateHash_SameTxidDifferentVout(t *testing.T) {
	// totalIn = 90000, totalOut = 50000, fee = 1000, change = 39000
	params1 := chain.WithdrawTemplateParams{
		Chain:       "btc",
		VaultID:     1,
		KeyEpoch:    1,
		WithdrawIDs: []string{"w1"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr", Amount: 50000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid1, Vout: 2, Amount: 30000},
			{TxID: txid1, Vout: 0, Amount: 30000},
			{TxID: txid1, Vout: 1, Amount: 30000},
		},
		ChangeAddress: "bc1qchange",
		Fee:           1000,
		ChangeAmount:  39000,
	}

	params2 := chain.WithdrawTemplateParams{
		Chain:       "btc",
		VaultID:     1,
		KeyEpoch:    1,
		WithdrawIDs: []string{"w1"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr", Amount: 50000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid1, Vout: 1, Amount: 30000}, // 不同顺序
			{TxID: txid1, Vout: 2, Amount: 30000},
			{TxID: txid1, Vout: 0, Amount: 30000},
		},
		ChangeAddress: "bc1qchange",
		Fee:           1000,
		ChangeAmount:  39000,
	}

	template1, _ := NewBTCTemplate(params1)
	template2, _ := NewBTCTemplate(params2)

	// 验证 vout 排序
	if template1.Inputs[0].Vout != 0 || template1.Inputs[1].Vout != 1 || template1.Inputs[2].Vout != 2 {
		t.Errorf("Inputs not sorted by vout: %+v", template1.Inputs)
	}

	// 验证 hash 相同
	if !bytes.Equal(template1.TemplateHash(), template2.TemplateHash()) {
		t.Error("TemplateHash should be same for different input order with same txid")
	}
}

// TestTemplateHash_JSONRoundTrip 测试 JSON 序列化/反序列化后 hash 一致
func TestTemplateHash_JSONRoundTrip(t *testing.T) {
	// totalIn = 100000, totalOut = 60000, fee = 2000, change = 38000
	params := chain.WithdrawTemplateParams{
		Chain:       "btc",
		VaultID:     5,
		KeyEpoch:    999,
		WithdrawIDs: []string{"w1", "w2", "w3"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr1", Amount: 10000},
			{WithdrawID: "w2", To: "bc1qaddr2", Amount: 20000},
			{WithdrawID: "w3", To: "bc1qaddr3", Amount: 30000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid2, Vout: 0, Amount: 100000},
		},
		ChangeAddress: "bc1qchange",
		Fee:           2000,
		ChangeAmount:  38000,
	}

	template1, err := NewBTCTemplate(params)
	if err != nil {
		t.Fatalf("NewBTCTemplate failed: %v", err)
	}

	hash1 := template1.TemplateHash()

	// 序列化为 JSON
	jsonData, err := template1.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	// 从 JSON 反序列化
	template2, err := FromJSON(jsonData)
	if err != nil {
		t.Fatalf("FromJSON failed: %v", err)
	}

	hash2 := template2.TemplateHash()

	// 验证 hash 一致
	if !bytes.Equal(hash1, hash2) {
		t.Errorf("TemplateHash mismatch after JSON roundtrip:\nhash1: %x\nhash2: %x", hash1, hash2)
	}
}

// TestTemplateHash_DifferentVaultID 测试不同 VaultID 产生不同 hash
func TestTemplateHash_DifferentVaultID(t *testing.T) {
	// totalIn = 50000, totalOut = 10000, fee = 1000, change = 39000
	baseParams := chain.WithdrawTemplateParams{
		Chain:       "btc",
		KeyEpoch:    1,
		WithdrawIDs: []string{"w1"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr", Amount: 10000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid1, Vout: 0, Amount: 50000},
		},
		ChangeAddress: "bc1qchange",
		Fee:           1000,
		ChangeAmount:  39000,
	}

	// VaultID = 1
	params1 := baseParams
	params1.VaultID = 1
	template1, _ := NewBTCTemplate(params1)

	// VaultID = 2
	params2 := baseParams
	params2.VaultID = 2
	template2, _ := NewBTCTemplate(params2)

	// 验证 hash 不同
	if bytes.Equal(template1.TemplateHash(), template2.TemplateHash()) {
		t.Error("TemplateHash should differ for different VaultID")
	}
}

// TestTemplateHash_DifferentKeyEpoch 测试不同 KeyEpoch 产生不同 hash
func TestTemplateHash_DifferentKeyEpoch(t *testing.T) {
	// totalIn = 50000, totalOut = 10000, fee = 1000, change = 39000
	baseParams := chain.WithdrawTemplateParams{
		Chain:       "btc",
		VaultID:     1,
		WithdrawIDs: []string{"w1"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr", Amount: 10000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid1, Vout: 0, Amount: 50000},
		},
		ChangeAddress: "bc1qchange",
		Fee:           1000,
		ChangeAmount:  39000,
	}

	// KeyEpoch = 1
	params1 := baseParams
	params1.KeyEpoch = 1
	template1, _ := NewBTCTemplate(params1)

	// KeyEpoch = 2
	params2 := baseParams
	params2.KeyEpoch = 2
	template2, _ := NewBTCTemplate(params2)

	// 验证 hash 不同
	if bytes.Equal(template1.TemplateHash(), template2.TemplateHash()) {
		t.Error("TemplateHash should differ for different KeyEpoch")
	}
}

// TestNewBTCTemplate_InputOutputMismatch 测试输入输出不匹配错误
func TestNewBTCTemplate_InputOutputMismatch(t *testing.T) {
	// totalIn = 10000, but totalOut + fee + change = 100000 + 1000 + 0 = 101000
	params := chain.WithdrawTemplateParams{
		Chain:       "btc",
		VaultID:     1,
		KeyEpoch:    1,
		WithdrawIDs: []string{"w1"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr", Amount: 100000}, // 需要 100000
		},
		Inputs: []chain.UTXO{
			{TxID: txid1, Vout: 0, Amount: 10000}, // 只有 10000
		},
		Fee:          1000,
		ChangeAmount: 0,
	}

	_, err := NewBTCTemplate(params)
	if err == nil {
		t.Error("Expected input/output mismatch error")
	}
}

// TestNewBTCTemplate_NoInputs 测试无输入错误
func TestNewBTCTemplate_NoInputs(t *testing.T) {
	params := chain.WithdrawTemplateParams{
		Chain:       "btc",
		VaultID:     1,
		KeyEpoch:    1,
		WithdrawIDs: []string{"w1"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w1", To: "bc1qaddr", Amount: 10000},
		},
		Inputs:       []chain.UTXO{}, // 空输入
		Fee:          1000,
		ChangeAmount: 0,
	}

	_, err := NewBTCTemplate(params)
	if err == nil {
		t.Error("Expected no inputs error")
	}
}

// TestNewBTCTemplate_OutputOrderMismatch 乱序输出必须报错
func TestNewBTCTemplate_OutputOrderMismatch(t *testing.T) {
	// totalIn = 50000, totalOut = 20000, fee = 1000, change = 29000
	params := chain.WithdrawTemplateParams{
		Chain:       "btc",
		VaultID:     1,
		KeyEpoch:    1,
		WithdrawIDs: []string{"w1", "w2"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "w2", To: "bc1qaddr2", Amount: 10000}, // 顺序错误
			{WithdrawID: "w1", To: "bc1qaddr1", Amount: 10000},
		},
		Inputs: []chain.UTXO{
			{TxID: txid1, Vout: 0, Amount: 50000},
		},
		ChangeAddress: "bc1qchange",
		Fee:           1000,
		ChangeAmount:  29000,
	}

	_, err := NewBTCTemplate(params)
	if err == nil {
		t.Error("Expected withdraw output order mismatch error")
	}
}
