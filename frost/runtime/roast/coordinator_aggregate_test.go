package roast

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	coreRoast "dex/frost/core/roast"
	"dex/frost/runtime/session"
	"dex/frost/runtime/types"
	"dex/logs"
	"dex/pb"
)

// ==========================================================================
// 测试 Coordinator.aggregateSignatures
// 在内存中完成完整的 FROST 门限签名流程，然后通过 Coordinator 聚合。
// ==========================================================================

// ---------- mock 依赖 ----------

type mockVaultProvider struct{ groupPubkey []byte }

func (m *mockVaultProvider) VaultCommittee(string, uint32, uint64) ([]SignerInfo, error) {
	return nil, nil
}
func (m *mockVaultProvider) VaultCurrentEpoch(string, uint32) uint64 { return 1 }
func (m *mockVaultProvider) VaultGroupPubkey(string, uint32, uint64) ([]byte, error) {
	return m.groupPubkey, nil
}
func (m *mockVaultProvider) CalculateThreshold(string, uint32) (int, error) { return 2, nil }

type mockMessenger struct{}

func (m *mockMessenger) Send(to NodeID, msg *types.RoastEnvelope) error           { return nil }
func (m *mockMessenger) Broadcast(peers []NodeID, msg *types.RoastEnvelope) error { return nil }

type mockCryptoFactory struct{}

func (f *mockCryptoFactory) NewROASTExecutor(signAlgo int32) (ROASTExecutor, error) {
	return newSecp256k1Executor(), nil
}
func (f *mockCryptoFactory) NewDKGExecutor(int32) (DKGExecutor, error) { return nil, nil }

// secp256k1 ROAST executor
type secp256k1Executor struct{ group curve.Group }

func newSecp256k1Executor() *secp256k1Executor {
	return &secp256k1Executor{group: curve.NewSecp256k1Group()}
}

func (e *secp256k1Executor) ComputeGroupCommitment(nonces []NonceInput, msg []byte) (CurvePoint, error) {
	cn := make([]coreRoast.SignerNonce, len(nonces))
	for i, n := range nonces {
		cn[i] = coreRoast.SignerNonce{SignerID: n.SignerID, HidingPoint: curve.Point(n.HidingPoint), BindingPoint: curve.Point(n.BindingPoint)}
	}
	p, err := coreRoast.ComputeGroupCommitment(cn, msg, e.group)
	return CurvePoint(p), err
}

func (e *secp256k1Executor) ComputeBindingCoefficient(signerID int, msg []byte, nonces []NonceInput) *big.Int {
	cn := make([]coreRoast.SignerNonce, len(nonces))
	for i, n := range nonces {
		cn[i] = coreRoast.SignerNonce{SignerID: n.SignerID, HidingPoint: curve.Point(n.HidingPoint), BindingPoint: curve.Point(n.BindingPoint)}
	}
	return coreRoast.ComputeBindingCoefficient(signerID, msg, cn, e.group)
}

func (e *secp256k1Executor) ComputeChallenge(R CurvePoint, groupPubX *big.Int, msg []byte) *big.Int {
	return coreRoast.ComputeChallenge(curve.Point(R), groupPubX, msg, e.group)
}

func (e *secp256k1Executor) ComputeLagrangeCoefficients(signerIDs []int) map[int]*big.Int {
	return coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, e.group.Order())
}

func (e *secp256k1Executor) ComputePartialSignature(params PartialSignParams) *big.Int {
	return coreRoast.ComputePartialSignature(params.SignerID, params.HidingNonce, params.BindingNonce, params.SecretShare, params.Rho, params.Lambda, params.Challenge, e.group)
}

func (e *secp256k1Executor) AggregateSignatures(R CurvePoint, shares []ShareInput) ([]byte, error) {
	cs := make([]coreRoast.SignerShare, len(shares))
	for i, s := range shares {
		cs[i] = coreRoast.SignerShare{SignerID: s.SignerID, Share: s.Share}
	}
	return coreRoast.AggregateSignatures(curve.Point(R), cs, e.group)
}

func (e *secp256k1Executor) GenerateNoncePair() (hiding, binding *big.Int, hidingPoint, bindingPoint CurvePoint, err error) {
	signer := NewSignerService("test", nil, nil)
	n, err := signer.GenerateNonce()
	if err != nil {
		return nil, nil, CurvePoint{}, CurvePoint{}, err
	}
	return n.HidingNonce, n.BindingNonce, CurvePoint(n.HidingPoint), CurvePoint(n.BindingPoint), nil
}

func (e *secp256k1Executor) ScalarBaseMult(k *big.Int) CurvePoint {
	return CurvePoint(e.group.ScalarBaseMult(k))
}

func (e *secp256k1Executor) SerializePoint(P CurvePoint) []byte {
	return e.group.SerializePoint(curve.Point(P))
}

// ---------- 辅助：生成 nonce 对 ----------

func generateNoncePairForTest(group curve.Group) (hiding, binding *big.Int, hp, bp curve.Point) {
	signer := NewSignerService("test", nil, nil)
	n, _ := signer.GenerateNonce()
	return n.HidingNonce, n.BindingNonce, n.HidingPoint, n.BindingPoint
}

// ---------- 辅助：创建 Coordinator ----------

func newTestCoordinator(groupPubSerialized []byte) *Coordinator {
	return NewCoordinator("test_coordinator", &mockMessenger{},
		&mockVaultProvider{groupPubkey: groupPubSerialized},
		&mockCryptoFactory{}, logs.NewNodeLogger("test", 100))
}

// ---------- 测试 ----------

func TestCoordinator_AggregateSignatures(t *testing.T) {
	group := curve.NewSecp256k1Group()
	signAlgo := int32(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340)

	// 3-of-5
	threshold, numSigners := 3, 5
	masterSecret := new(big.Int).SetBytes([]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0})
	groupPub := group.ScalarBaseMult(masterSecret)
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)

	msg := sha256.Sum256([]byte("test aggregate signatures"))
	messages := [][]byte{msg[:]}
	signerIDs := []int{1, 2, 3, 4} // threshold+1

	committee := make([]session.Participant, numSigners)
	for i := 0; i < numSigners; i++ {
		committee[i] = session.Participant{ID: "node", Index: i, Address: "addr"}
	}

	sess := session.NewSession(session.SessionParams{
		JobID: "test_job", VaultID: 1, Chain: "btc", KeyEpoch: 1,
		SignAlgo: signAlgo, Messages: messages, Committee: committee, Threshold: threshold,
	})
	sess.Start()
	sess.SelectedSet = []int{0, 1, 2, 3}
	sess.SetState(session.SignSessionStateCollectingNonces)

	// 生成 nonce
	type nonceInfo struct {
		h, b   *big.Int
		hp, bp curve.Point
	}
	signerNonces := make(map[int]*nonceInfo)
	for _, sid := range signerIDs {
		h, b, hp, bp := generateNoncePairForTest(group)
		signerNonces[sid] = &nonceInfo{h, b, hp, bp}
		sess.AddNonce(sid-1, [][]byte{group.SerializePoint(hp)}, [][]byte{group.SerializePoint(bp)})
	}

	sess.SetState(session.SignSessionStateCollectingShares)

	// 计算部分签名
	coreNonces := make([]coreRoast.SignerNonce, len(signerIDs))
	for i, sid := range signerIDs {
		n := signerNonces[sid]
		coreNonces[i] = coreRoast.SignerNonce{SignerID: sid, HidingNonce: n.h, BindingNonce: n.b, HidingPoint: n.hp, BindingPoint: n.bp}
	}
	R, _ := coreRoast.ComputeGroupCommitment(coreNonces, msg[:], group)
	e := coreRoast.ComputeChallenge(R, groupPub.X, msg[:], group)
	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())

	for _, sid := range signerIDs {
		n := signerNonces[sid]
		rho := coreRoast.ComputeBindingCoefficient(sid, msg[:], coreNonces, group)
		z := coreRoast.ComputePartialSignature(sid, n.h, n.b, shares[sid], rho, lambdas[sid], e, group)
		sess.AddShare(sid-1, [][]byte{z.Bytes()})
	}

	// 聚合
	coordinator := newTestCoordinator(group.SerializePoint(groupPub))
	signatures, err := coordinator.aggregateSignatures(sess)
	if err != nil {
		t.Fatalf("aggregateSignatures failed: %v", err)
	}
	if len(signatures) != 1 || signatures[0] == nil || len(signatures[0]) != 64 {
		t.Fatalf("unexpected signature: len=%d", len(signatures))
	}
	if sess.GetState() != session.SignSessionStateComplete {
		t.Fatalf("expected COMPLETE, got %s", sess.GetState())
	}
	t.Logf("✅ 单消息 aggregateSignatures 通过: sig=%x", signatures[0][:16])
}

func TestCoordinator_AggregateSignatures_MultiMessage(t *testing.T) {
	group := curve.NewSecp256k1Group()
	signAlgo := int32(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340)

	threshold, numSigners := 2, 3
	masterSecret := new(big.Int).SetBytes([]byte{0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89})
	groupPub := group.ScalarBaseMult(masterSecret)
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)

	msg1 := sha256.Sum256([]byte("btc sighash input 0"))
	msg2 := sha256.Sum256([]byte("btc sighash input 1"))
	messages := [][]byte{msg1[:], msg2[:]}
	signerIDs := []int{1, 2, 3}

	committee := make([]session.Participant, numSigners)
	for i := range committee {
		committee[i] = session.Participant{ID: "n", Index: i}
	}

	sess := session.NewSession(session.SessionParams{
		JobID: "test_multi", VaultID: 1, Chain: "btc", KeyEpoch: 1,
		SignAlgo: signAlgo, Messages: messages, Committee: committee, Threshold: threshold,
	})
	sess.Start()
	sess.SelectedSet = []int{0, 1, 2}
	sess.SetState(session.SignSessionStateCollectingNonces)

	// 每个 signer 对每个 task 生成 nonce
	type ptn struct {
		h, b   *big.Int
		hp, bp curve.Point
	}
	signerNonces := make(map[int][]*ptn) // sid -> [task0, task1]

	for _, sid := range signerIDs {
		taskNonces := make([]*ptn, len(messages))
		hidingSer, bindingSer := make([][]byte, len(messages)), make([][]byte, len(messages))
		for ti := range messages {
			h, b, hp, bp := generateNoncePairForTest(group)
			taskNonces[ti] = &ptn{h, b, hp, bp}
			hidingSer[ti] = group.SerializePoint(hp)
			bindingSer[ti] = group.SerializePoint(bp)
		}
		signerNonces[sid] = taskNonces
		sess.AddNonce(sid-1, hidingSer, bindingSer)
	}
	sess.SetState(session.SignSessionStateCollectingShares)

	// 计算部分签名（按 task）
	allShares := make(map[int][][]byte) // sid -> [task0_share, task1_share]
	for _, sid := range signerIDs {
		allShares[sid] = make([][]byte, len(messages))
	}

	for ti, msg := range messages {
		cn := make([]coreRoast.SignerNonce, len(signerIDs))
		for i, sid := range signerIDs {
			n := signerNonces[sid][ti]
			cn[i] = coreRoast.SignerNonce{SignerID: sid, HidingNonce: n.h, BindingNonce: n.b, HidingPoint: n.hp, BindingPoint: n.bp}
		}
		R, _ := coreRoast.ComputeGroupCommitment(cn, msg, group)
		e := coreRoast.ComputeChallenge(R, groupPub.X, msg, group)
		lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())

		for _, sid := range signerIDs {
			n := signerNonces[sid][ti]
			rho := coreRoast.ComputeBindingCoefficient(sid, msg, cn, group)
			z := coreRoast.ComputePartialSignature(sid, n.h, n.b, shares[sid], rho, lambdas[sid], e, group)
			allShares[sid][ti] = z.Bytes()
		}
	}

	for _, sid := range signerIDs {
		sess.AddShare(sid-1, allShares[sid])
	}

	coordinator := newTestCoordinator(group.SerializePoint(groupPub))
	signatures, err := coordinator.aggregateSignatures(sess)
	if err != nil {
		t.Fatalf("aggregateSignatures multi-message failed: %v", err)
	}
	for i, sig := range signatures {
		if sig == nil || len(sig) != 64 {
			t.Fatalf("signature[%d] invalid", i)
		}
		t.Logf("  task[%d] sig=%x", i, sig[:16])
	}
	t.Logf("✅ 多消息 aggregateSignatures 通过: %d 个签名", len(signatures))
}

func TestCoordinator_AggregateSignatures_InsufficientShares(t *testing.T) {
	msg := sha256.Sum256([]byte("test"))
	sess := session.NewSession(session.SessionParams{
		JobID: "test_insuf", VaultID: 1, Chain: "btc", KeyEpoch: 1,
		SignAlgo:  int32(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340),
		Messages:  [][]byte{msg[:]},
		Committee: []session.Participant{{ID: "a", Index: 0}, {ID: "b", Index: 1}, {ID: "c", Index: 2}},
		Threshold: 2,
	})
	sess.Start()
	sess.SelectedSet = []int{0, 1, 2}

	coordinator := newTestCoordinator(nil)
	_, err := coordinator.aggregateSignatures(sess)
	if err != ErrInsufficientShares {
		t.Fatalf("expected ErrInsufficientShares, got: %v", err)
	}
	t.Logf("✅ 份额不足正确返回错误: %v", err)
}
