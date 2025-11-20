package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"dex/types"
	"testing"
)

// TestVRFGenerateAndVerify 测试VRF生成和验证的完整流程
func TestVRFGenerateAndVerify(t *testing.T) {
	// 1. 生成ECDSA密钥对
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate ECDSA key: %v", err)
	}

	// 2. 创建VRF提供者
	vrfProvider := NewVRFProvider()

	// 3. 测试参数
	height := uint64(1)
	window := 2
	parentBlockHash := "genesis"
	nodeID := types.NodeID("test-node-1")

	// 4. 生成VRF
	t.Logf("Generating VRF with:")
	t.Logf("  Height: %d", height)
	t.Logf("  Window: %d", window)
	t.Logf("  ParentHash: %s", parentBlockHash)
	t.Logf("  NodeID: %s", nodeID)

	vrfOutput, vrfProof, err := vrfProvider.GenerateVRF(privateKey, height, window, parentBlockHash, nodeID)
	if err != nil {
		t.Fatalf("Failed to generate VRF: %v", err)
	}

	t.Logf("VRF generated successfully:")
	t.Logf("  Output length: %d bytes", len(vrfOutput))
	t.Logf("  Proof length: %d bytes", len(vrfProof))
	t.Logf("  Output: %x", vrfOutput)
	t.Logf("  Proof: %x", vrfProof)

	// 5. 验证VRF（使用BLS公钥）
	t.Logf("\nVerifying VRF with BLS public key...")

	// 从ECDSA私钥获取BLS公钥
	blsPublicKey, err := GetBLSPublicKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to get BLS public key: %v", err)
	}

	err = vrfProvider.VerifyVRFWithBLSPublicKey(blsPublicKey, height, window, parentBlockHash, nodeID, vrfProof, vrfOutput)
	if err != nil {
		t.Fatalf("VRF verification failed: %v", err)
	}

	t.Logf("✓ VRF verification succeeded!")
}

// TestVRFWithDifferentKeys 测试使用不同密钥时VRF验证应该失败
func TestVRFWithDifferentKeys(t *testing.T) {
	// 生成两个不同的密钥对
	privateKey1, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	privateKey2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	publicKey2 := &privateKey2.PublicKey

	vrfProvider := NewVRFProvider()

	height := uint64(1)
	window := 2
	parentBlockHash := "genesis"
	nodeID := types.NodeID("test-node-1")

	// 使用privateKey1生成VRF
	vrfOutput, vrfProof, err := vrfProvider.GenerateVRF(privateKey1, height, window, parentBlockHash, nodeID)
	if err != nil {
		t.Fatalf("Failed to generate VRF: %v", err)
	}

	// 使用publicKey2验证（应该失败）
	err = vrfProvider.VerifyVRF(publicKey2, height, window, parentBlockHash, nodeID, vrfProof, vrfOutput)
	if err == nil {
		t.Fatalf("VRF verification should have failed with different key, but succeeded")
	}

	t.Logf("✓ VRF correctly failed verification with different key: %v", err)
}

// TestVRFWithDifferentParameters 测试使用不同参数时VRF验证应该失败
func TestVRFWithDifferentParameters(t *testing.T) {
	privateKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	publicKey := &privateKey.PublicKey

	vrfProvider := NewVRFProvider()

	height := uint64(1)
	window := 2
	parentBlockHash := "genesis"
	nodeID := types.NodeID("test-node-1")

	// 生成VRF
	vrfOutput, vrfProof, err := vrfProvider.GenerateVRF(privateKey, height, window, parentBlockHash, nodeID)
	if err != nil {
		t.Fatalf("Failed to generate VRF: %v", err)
	}

	// 测试1: 使用不同的height验证（应该失败）
	err = vrfProvider.VerifyVRF(publicKey, height+1, window, parentBlockHash, nodeID, vrfProof, vrfOutput)
	if err == nil {
		t.Errorf("VRF verification should have failed with different height")
	} else {
		t.Logf("✓ Correctly failed with different height: %v", err)
	}

	// 测试2: 使用不同的window验证（应该失败）
	err = vrfProvider.VerifyVRF(publicKey, height, window+1, parentBlockHash, nodeID, vrfProof, vrfOutput)
	if err == nil {
		t.Errorf("VRF verification should have failed with different window")
	} else {
		t.Logf("✓ Correctly failed with different window: %v", err)
	}

	// 测试3: 使用不同的parentBlockHash验证（应该失败）
	err = vrfProvider.VerifyVRF(publicKey, height, window, "different-parent", nodeID, vrfProof, vrfOutput)
	if err == nil {
		t.Errorf("VRF verification should have failed with different parent hash")
	} else {
		t.Logf("✓ Correctly failed with different parent hash: %v", err)
	}

	// 测试4: 使用不同的nodeID验证（应该失败）
	err = vrfProvider.VerifyVRF(publicKey, height, window, parentBlockHash, types.NodeID("different-node"), vrfProof, vrfOutput)
	if err == nil {
		t.Errorf("VRF verification should have failed with different nodeID")
	} else {
		t.Logf("✓ Correctly failed with different nodeID: %v", err)
	}
}

// TestVRFKeyDerivation 测试BLS密钥派生的一致性
func TestVRFKeyDerivation(t *testing.T) {
	// 生成ECDSA密钥对
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate ECDSA key: %v", err)
	}

	// 测试BLS私钥派生
	blsPrivKey1, err := GetBLSPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to derive BLS private key (1st time): %v", err)
	}

	blsPrivKey2, err := GetBLSPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to derive BLS private key (2nd time): %v", err)
	}

	// 两次派生应该得到相同的结果
	if blsPrivKey1.String() != blsPrivKey2.String() {
		t.Errorf("BLS private key derivation is not deterministic")
	} else {
		t.Logf("✓ BLS private key derivation is deterministic")
	}

	// 测试BLS公钥派生
	publicKey := &privateKey.PublicKey
	blsPubKey1, err := GetBLSPublicKeyFromECDSAPublic(publicKey)
	if err != nil {
		t.Fatalf("Failed to derive BLS public key (1st time): %v", err)
	}

	blsPubKey2, err := GetBLSPublicKeyFromECDSAPublic(publicKey)
	if err != nil {
		t.Fatalf("Failed to derive BLS public key (2nd time): %v", err)
	}

	// 两次派生应该得到相同的结果
	if blsPubKey1.String() != blsPubKey2.String() {
		t.Errorf("BLS public key derivation is not deterministic")
	} else {
		t.Logf("✓ BLS public key derivation is deterministic")
	}

	t.Logf("BLS Private Key: %s", blsPrivKey1.String())
	t.Logf("BLS Public Key: %s", blsPubKey1.String())
}

// TestVRFRealScenario 模拟真实场景：提案者生成VRF，验证者验证
func TestVRFRealScenario(t *testing.T) {
	t.Log("=== Simulating Real VRF Scenario ===")

	// 场景：节点A生成区块提案，节点B验证

	// 1. 节点A的密钥（提案者）
	proposerPrivKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	// 2. 区块参数
	height := uint64(100)
	window := 2
	parentBlockHash := "block-99-hash"
	proposerNodeID := types.NodeID("bc1qxdr4f5mr230u2j6hu2pv4zpvr29ade8vxzvgx3")

	t.Logf("\n--- Proposer (Node A) ---")
	t.Logf("Proposer NodeID: %s", proposerNodeID)
	t.Logf("Block Height: %d", height)
	t.Logf("Window: %d", window)
	t.Logf("Parent Hash: %s", parentBlockHash)

	// 3. 节点A生成VRF
	vrfProvider := NewVRFProvider()
	vrfOutput, vrfProof, err := vrfProvider.GenerateVRF(
		proposerPrivKey,
		height,
		window,
		parentBlockHash,
		proposerNodeID,
	)
	if err != nil {
		t.Fatalf("Proposer failed to generate VRF: %v", err)
	}

	t.Logf("\nVRF Generated:")
	t.Logf("  Output: %x", vrfOutput)
	t.Logf("  Proof: %x", vrfProof)

	// 4. 节点A将区块（包含VRF proof和output）广播到网络
	// 模拟区块数据
	block := struct {
		Height    uint64
		Window    int
		ParentID  string
		Proposer  string
		VRFProof  []byte
		VRFOutput []byte
	}{
		Height:    height,
		Window:    window,
		ParentID:  parentBlockHash,
		Proposer:  string(proposerNodeID),
		VRFProof:  vrfProof,
		VRFOutput: vrfOutput,
	}

	t.Logf("\n--- Validator (Node B) ---")
	t.Logf("Received block from proposer: %s", block.Proposer)

	// 5. 节点B验证VRF
	// 在真实场景中，节点B需要从区块中获取提案者的BLS公钥
	// 首先从提案者的ECDSA私钥获取BLS公钥（模拟区块中存储的BLS公钥）
	proposerBLSPubKey, err := GetBLSPublicKey(proposerPrivKey)
	if err != nil {
		t.Fatalf("Failed to get proposer's BLS public key: %v", err)
	}

	// 使用BLS公钥验证VRF（这是推荐的方法）
	err = vrfProvider.VerifyVRFWithBLSPublicKey(
		proposerBLSPubKey,
		block.Height,
		block.Window,
		block.ParentID,
		types.NodeID(block.Proposer),
		block.VRFProof,
		block.VRFOutput,
	)

	if err != nil {
		t.Fatalf("Validator failed to verify VRF: %v", err)
	}

	t.Logf("✓ VRF verification succeeded!")
	t.Logf("\n=== Real Scenario Test Passed ===")
}

// TestVRFProbabilityCalculation 测试出块概率计算
func TestVRFProbabilityCalculation(t *testing.T) {
	privateKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	vrfProvider := NewVRFProvider()

	height := uint64(1)
	window := 2
	parentBlockHash := "genesis"
	nodeID := types.NodeID("test-node")

	// 生成VRF
	vrfOutput, _, err := vrfProvider.GenerateVRF(privateKey, height, window, parentBlockHash, nodeID)
	if err != nil {
		t.Fatalf("Failed to generate VRF: %v", err)
	}

	// 测试不同的概率阈值
	thresholds := []float64{0.05, 0.15, 0.30, 0.50, 1.0}
	
	t.Logf("VRF Output: %x", vrfOutput)
	
	for _, threshold := range thresholds {
		shouldPropose := CalculateBlockProbability(vrfOutput, threshold)
		t.Logf("Threshold %.2f: Should propose = %v", threshold, shouldPropose)
	}
}

