package consensus

import (
	"dex/pb"
	"dex/types"
	"dex/utils"
	"testing"
)

// ============================================
// VRF 确定性采样 + 同步签名验证 测试
// ============================================

// --- 确定性采样测试 ---

// TestSamplePeersDeterministic_SameSeedSameResult 同 Seed+SeqID 应输出相同节点集
func TestSamplePeersDeterministic_SameSeedSameResult(t *testing.T) {
	seed := computeVRFSeed("parent-block-abc", 10, 0, "node-01")
	peers := makePeerList(20)
	k := 5

	result1 := samplePeersDeterministic(seed, 0, k, peers)
	result2 := samplePeersDeterministic(seed, 0, k, peers)

	if len(result1) != k || len(result2) != k {
		t.Fatalf("expected %d peers, got %d and %d", k, len(result1), len(result2))
	}
	for i := range result1 {
		if result1[i] != result2[i] {
			t.Errorf("mismatch at index %d: %s vs %s", i, result1[i], result2[i])
		}
	}
}

// TestSamplePeersDeterministic_InputOrderIndependent 同一节点集合（不同顺序）应输出一致结果
func TestSamplePeersDeterministic_InputOrderIndependent(t *testing.T) {
	seed := computeVRFSeed("parent-block-abc", 10, 0, "node-01")
	peersA := makePeerList(20)
	peersB := make([]types.NodeID, len(peersA))
	copy(peersB, peersA)

	// Reverse to simulate map iteration / random snapshot order.
	for i, j := 0, len(peersB)-1; i < j; i, j = i+1, j-1 {
		peersB[i], peersB[j] = peersB[j], peersB[i]
	}

	k := 6
	resultA := samplePeersDeterministic(seed, 3, k, peersA)
	resultB := samplePeersDeterministic(seed, 3, k, peersB)

	if len(resultA) != len(resultB) {
		t.Fatalf("expected same result length, got %d and %d", len(resultA), len(resultB))
	}
	for i := range resultA {
		if resultA[i] != resultB[i] {
			t.Fatalf("order-dependent result at index %d: %s vs %s", i, resultA[i], resultB[i])
		}
	}
}

// TestSamplePeersDeterministic_DiffSeqIDDiffResult 换 SeqID 应输出不同节点集
func TestSamplePeersDeterministic_DiffSeqIDDiffResult(t *testing.T) {
	seed := computeVRFSeed("parent-block-abc", 10, 0, "node-01")
	peers := makePeerList(20)
	k := 5

	result1 := samplePeersDeterministic(seed, 0, k, peers)
	result2 := samplePeersDeterministic(seed, 1, k, peers)

	if len(result1) != k || len(result2) != k {
		t.Fatalf("expected %d peers, got %d and %d", k, len(result1), len(result2))
	}

	allSame := true
	for i := range result1 {
		if result1[i] != result2[i] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("different SeqIDs produced identical peer sets — extremely unlikely, possible bug")
	}
}

// TestSamplePeersDeterministic_DiffSeedDiffResult 换 Seed 应输出不同节点集
func TestSamplePeersDeterministic_DiffSeedDiffResult(t *testing.T) {
	seed1 := computeVRFSeed("parent-block-aaa", 10, 0, "node-01")
	seed2 := computeVRFSeed("parent-block-bbb", 10, 0, "node-01")
	peers := makePeerList(20)
	k := 5

	result1 := samplePeersDeterministic(seed1, 0, k, peers)
	result2 := samplePeersDeterministic(seed2, 0, k, peers)

	allSame := true
	for i := range result1 {
		if result1[i] != result2[i] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("different seeds produced identical peer sets — extremely unlikely, possible bug")
	}
}

// TestSamplePeersDeterministic_KGreaterThanPeers k 大于节点数时返回全部
func TestSamplePeersDeterministic_KGreaterThanPeers(t *testing.T) {
	seed := computeVRFSeed("parent", 1, 0, "node-01")
	peers := makePeerList(3)

	result := samplePeersDeterministic(seed, 0, 10, peers)
	if len(result) != 3 {
		t.Errorf("expected 3 peers (all), got %d", len(result))
	}
}

// TestSamplePeersDeterministic_EmptyPeers 空列表返回 nil
func TestSamplePeersDeterministic_EmptyPeers(t *testing.T) {
	seed := computeVRFSeed("parent", 1, 0, "node-01")
	result := samplePeersDeterministic(seed, 0, 5, nil)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

// TestComputeVRFSeed_Deterministic 相同输入产生相同种子
func TestComputeVRFSeed_Deterministic(t *testing.T) {
	s1 := computeVRFSeed("block-123", 42, 0, "node-05")
	s2 := computeVRFSeed("block-123", 42, 0, "node-05")

	if len(s1) != 32 {
		t.Fatalf("expected 32 bytes SHA256, got %d", len(s1))
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			t.Fatal("same inputs produced different seeds")
		}
	}
}

// TestComputeVRFSeed_DiffInputsDiffOutput 不同输入产生不同种子
func TestComputeVRFSeed_DiffInputsDiffOutput(t *testing.T) {
	s1 := computeVRFSeed("block-123", 42, 0, "node-05")
	s2 := computeVRFSeed("block-123", 42, 0, "node-06")

	allSame := true
	for i := range s1 {
		if s1[i] != s2[i] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("different NodeID produced same seed")
	}
}

// --- 同步签名验证测试 ---

// mockTransport 用于测试的简易 Transport 实现
type mockTransport struct {
	peers []types.NodeID
}

func (m *mockTransport) Send(to types.NodeID, msg types.Message) error     { return nil }
func (m *mockTransport) Receive() <-chan types.Message                     { return nil }
func (m *mockTransport) Broadcast(msg types.Message, peers []types.NodeID) {}
func (m *mockTransport) SamplePeers(exclude types.NodeID, count int) []types.NodeID {
	return m.peers
}
func (m *mockTransport) GetAllPeers(exclude types.NodeID) []types.NodeID {
	var result []types.NodeID
	for _, p := range m.peers {
		if p != exclude {
			result = append(result, p)
		}
	}
	return result
}

// newTestKeyManager 创建测试用的 KeyManager（随机密钥）
func newTestKeyManager(t *testing.T) *utils.KeyManager {
	t.Helper()
	km := utils.NewKeyManager()
	if err := km.InitKeyRandom(); err != nil {
		t.Fatalf("InitKeyRandom failed: %v", err)
	}
	return km
}

// TestVerifySignatureSet_Valid 合法签名集合应通过验证（使用真 ECDSA 签名）
func TestVerifySignatureSet_Valid(t *testing.T) {
	ClearNodePublicKeys()
	defer ClearNodePublicKeys()

	peers := makePeerList(20)
	transport := &mockTransport{peers: peers}
	localNodeID := types.NodeID("node-verifier")
	alpha := 3
	beta := 2

	// 为每个 peer 生成密钥对并注册
	signers := make(map[types.NodeID]*utils.KeyManager)
	for _, p := range peers {
		km := newTestKeyManager(t)
		signers[p] = km
		RegisterNodePublicKey(p, km.PublicKeyBytes())
	}

	seed := computeVRFSeed("parent-block", 10, 0, "node-proposer")

	// 构造合法的签名集合：每轮从确定性采样结果中选取签名者，使用真 ECDSA 签名
	rounds := make([]*pb.RoundSignatures, beta)
	for i := 0; i < beta; i++ {
		seqID := uint32(i)
		sampled := samplePeersDeterministic(seed, seqID, 5, peers)

		sigs := make([]*pb.ChitSignature, alpha)
		for j := 0; j < alpha; j++ {
			peerID := sampled[j]
			digest := ComputeChitDigest("block-10", 10, seed, seqID)
			sig, err := signers[peerID].Sign(digest)
			if err != nil {
				t.Fatalf("Sign failed: %v", err)
			}
			sigs[j] = &pb.ChitSignature{
				NodeId:      string(peerID),
				PreferredId: "block-10",
				Signature:   sig,
			}
		}
		rounds[i] = &pb.RoundSignatures{
			SeqId:      seqID,
			Signatures: sigs,
		}
	}

	sigSet := &pb.ConsensusSignatureSet{
		BlockId:  "block-10",
		Height:   10,
		ParentId: "parent-block",
		VrfSeed:  seed,
		Rounds:   rounds,
	}

	if !VerifySignatureSet(sigSet, alpha, beta, transport, localNodeID) {
		t.Error("valid ECDSA-signed signature set should pass verification")
	}
}

// TestVerifySignatureSet_Nil nil 签名集合应失败
func TestVerifySignatureSet_Nil(t *testing.T) {
	if VerifySignatureSet(nil, 3, 2, nil, "node-01") {
		t.Error("nil signature set should fail verification")
	}
}

// TestVerifySignatureSet_LocalSignerIncluded 验证节点自己出现在签名集合中时应允许通过
func TestVerifySignatureSet_LocalSignerIncluded(t *testing.T) {
	ClearNodePublicKeys()
	defer ClearNodePublicKeys()

	transport := &mockTransport{
		peers: []types.NodeID{"node-01", "node-02", "node-03"},
	}
	sigSet := &pb.ConsensusSignatureSet{
		BlockId:  "block-7",
		Height:   7,
		ParentId: "block-6",
		VrfSeed:  []byte("seed-7"),
		Rounds: []*pb.RoundSignatures{
			{
				SeqId: 0,
				Signatures: []*pb.ChitSignature{
					{
						NodeId:      "node-01",
						PreferredId: "block-7",
					},
				},
			},
		},
	}

	if !VerifySignatureSet(sigSet, 1, 1, transport, "node-01") {
		t.Fatal("local node as signer should not be rejected")
	}
}

// TestVerifySignatureSet_InsufficientRounds 轮次不足应失败
func TestVerifySignatureSet_InsufficientRounds(t *testing.T) {
	sigSet := &pb.ConsensusSignatureSet{
		BlockId: "block-10",
		Rounds: []*pb.RoundSignatures{
			{SeqId: 0, Signatures: []*pb.ChitSignature{{NodeId: "n1"}}},
		},
	}

	if VerifySignatureSet(sigSet, 1, 3, nil, "node-01") {
		t.Error("insufficient rounds should fail verification (1 < beta=3)")
	}
}

// TestVerifySignatureSet_InsufficientSignatures 签名数不足应失败
func TestVerifySignatureSet_InsufficientSignatures(t *testing.T) {
	sigSet := &pb.ConsensusSignatureSet{
		BlockId: "block-10",
		Rounds: []*pb.RoundSignatures{
			{SeqId: 0, Signatures: []*pb.ChitSignature{{NodeId: "n1"}}},
			{SeqId: 1, Signatures: []*pb.ChitSignature{{NodeId: "n2"}}},
		},
	}

	if VerifySignatureSet(sigSet, 3, 2, nil, "node-01") {
		t.Error("insufficient signatures per round should fail (1 < alpha=3)")
	}
}

// TestVerifySignatureSet_InvalidSigner 非法签名者应失败
func TestVerifySignatureSet_InvalidSigner(t *testing.T) {
	peers := makePeerList(20)
	transport := &mockTransport{peers: peers}
	seed := computeVRFSeed("parent-block", 10, 0, "node-proposer")

	rounds := []*pb.RoundSignatures{
		{
			SeqId: 0,
			Signatures: []*pb.ChitSignature{
				{NodeId: "ATTACKER-NOT-IN-NETWORK", PreferredId: "block-10", Signature: []byte("fake")},
				{NodeId: "ATTACKER-2-NOT-IN-NETWORK", PreferredId: "block-10", Signature: []byte("fake")},
				{NodeId: "ATTACKER-3-NOT-IN-NETWORK", PreferredId: "block-10", Signature: []byte("fake")},
			},
		},
	}

	sigSet := &pb.ConsensusSignatureSet{
		BlockId:  "block-10",
		Height:   10,
		ParentId: "parent-block",
		VrfSeed:  seed,
		Rounds:   rounds,
	}

	if VerifySignatureSet(sigSet, 3, 1, transport, "node-verifier") {
		t.Error("signature from non-sampled node should fail verification")
	}
}

// TestVerifySignatureSet_ZeroAlphaBeta alpha/beta 为 0 使用宽松默认值
func TestVerifySignatureSet_ZeroAlphaBeta(t *testing.T) {
	sigSet := &pb.ConsensusSignatureSet{
		BlockId: "block-10",
		Rounds: []*pb.RoundSignatures{
			{SeqId: 0, Signatures: []*pb.ChitSignature{{NodeId: "n1"}}},
		},
	}

	if !VerifySignatureSet(sigSet, 0, 0, nil, "node-01") {
		t.Error("zero alpha/beta should use lenient defaults and pass")
	}
}

// --- ECDSA 签名/验签专用测试 ---

// TestECDSASigner_SignAndVerify 签名-验签往返测试
func TestECDSASigner_SignAndVerify(t *testing.T) {
	ClearNodePublicKeys()
	defer ClearNodePublicKeys()

	km := newTestKeyManager(t)

	nodeID := "test-node"
	RegisterNodePublicKey(types.NodeID(nodeID), km.PublicKeyBytes())

	digest := ComputeChitDigest("block-42", 42, []byte("seed"), 7)
	sig, err := km.Sign(digest)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	if len(sig) != 64 {
		t.Fatalf("expected 64 byte signature, got %d", len(sig))
	}

	if !VerifyChitSignature(nodeID, digest, sig) {
		t.Error("valid ECDSA signature should verify successfully")
	}
}

// TestECDSASigner_ForgeryRejected 伪造签名应被拒绝
func TestECDSASigner_ForgeryRejected(t *testing.T) {
	ClearNodePublicKeys()
	defer ClearNodePublicKeys()

	km := newTestKeyManager(t)

	nodeID := "honest-node"
	RegisterNodePublicKey(types.NodeID(nodeID), km.PublicKeyBytes())

	digest := ComputeChitDigest("block-42", 42, []byte("seed"), 7)

	// 伪造签名：全零字节
	fakeSig := make([]byte, 64)
	if VerifyChitSignature(nodeID, digest, fakeSig) {
		t.Error("forged zero signature should be rejected")
	}

	// 用另一个节点的密钥签名
	otherKm := newTestKeyManager(t)
	otherSig, _ := otherKm.Sign(digest)
	if VerifyChitSignature(nodeID, digest, otherSig) {
		t.Error("signature from different key should be rejected")
	}
}

// TestVerifySignatureSet_ForgeryRejected 包含伪造签名的签名集合应失败（第四步验签）
func TestVerifySignatureSet_ForgeryRejected(t *testing.T) {
	ClearNodePublicKeys()
	defer ClearNodePublicKeys()

	peers := makePeerList(10)
	transport := &mockTransport{peers: peers}

	// 为每个 peer 注册密钥
	for _, p := range peers {
		km := newTestKeyManager(t)
		RegisterNodePublicKey(p, km.PublicKeyBytes())
	}

	seed := computeVRFSeed("parent", 5, 0, "proposer")
	sampled := samplePeersDeterministic(seed, 0, 5, peers)

	// 构造签名集合，签名使用伪造的数据
	sigs := make([]*pb.ChitSignature, 3)
	for j := 0; j < 3; j++ {
		sigs[j] = &pb.ChitSignature{
			NodeId:      string(sampled[j]),
			PreferredId: "block-5",
			Signature:   make([]byte, 64), // 全零签名 = 伪造
		}
	}

	sigSet := &pb.ConsensusSignatureSet{
		BlockId: "block-5",
		Height:  5,
		VrfSeed: seed,
		Rounds:  []*pb.RoundSignatures{{SeqId: 0, Signatures: sigs}},
	}

	if VerifySignatureSet(sigSet, 3, 1, transport, "verifier") {
		t.Error("forged ECDSA signatures should fail step-4 verification")
	}
}

// --- 辅助函数 ---

func makePeerList(n int) []types.NodeID {
	peers := make([]types.NodeID, n)
	for i := 0; i < n; i++ {
		peers[i] = types.NodeID(types.NodeID("peer-" + padInt(i)))
	}
	return peers
}

func padInt(n int) string {
	s := ""
	if n < 10 {
		s = "0"
	}
	return s + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
