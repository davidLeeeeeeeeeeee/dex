package roast

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	coreRoast "dex/frost/core/roast"
	"dex/frost/runtime/session"
	"dex/frost/runtime/types"
	"dex/logs"
	"dex/pb"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// ==========================================================================
// 测试 Coordinator.aggregateSignatures
// 在内存中完成完整的 FROST 门限签名流程，然后通过 Coordinator 聚合。
// ==========================================================================

// bip340AdjustPartialSig 模拟 Participant 端 BIP340 parity 调整后计算 partial signature
func bip340AdjustPartialSig(
	signerID int,
	hidingNonce, bindingNonce, share *big.Int,
	rho, lambda, e *big.Int,
	R curve.Point, groupPubSerialized []byte,
	group curve.Group,
) *big.Int {
	normShare := normalizeSecretShareForBIP340(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, groupPubSerialized, new(big.Int).Set(share))
	adjH, adjB := adjustNoncePairForBIP340(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, CurvePoint(R), hidingNonce, bindingNonce)
	return coreRoast.ComputePartialSignature(signerID, adjH, adjB, normShare, rho, lambda, e, group)
}

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
	return coreRoast.AggregateSignaturesBIP340(curve.Point(R), cs, e.group)
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

func mustHex(t *testing.T, v string) []byte {
	t.Helper()
	out, err := hex.DecodeString(v)
	if err != nil {
		t.Fatalf("decode hex failed: %v", err)
	}
	return out
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
	groupPubSerialized := group.SerializePoint(groupPub)

	for _, sid := range signerIDs {
		n := signerNonces[sid]
		rho := coreRoast.ComputeBindingCoefficient(sid, msg[:], coreNonces, group)
		z := bip340AdjustPartialSig(sid, n.h, n.b, shares[sid], rho, lambdas[sid], e, R, groupPubSerialized, group)
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
		groupPubSerialized := group.SerializePoint(groupPub)

		for _, sid := range signerIDs {
			n := signerNonces[sid][ti]
			rho := coreRoast.ComputeBindingCoefficient(sid, msg, cn, group)
			z := bip340AdjustPartialSig(sid, n.h, n.b, shares[sid], rho, lambdas[sid], e, R, groupPubSerialized, group)
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

func TestCoordinator_VerifyAggregatedSignature(t *testing.T) {
	group := curve.NewSecp256k1Group()
	signAlgo := pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340

	t.Run("basic_valid_and_invalid_paths", func(t *testing.T) {
		msg := sha256.Sum256([]byte("verify aggregated signature"))
		privKey, err := btcec.NewPrivateKey()
		if err != nil {
			t.Fatalf("new private key failed: %v", err)
		}
		sig, err := schnorr.Sign(privKey, msg[:])
		if err != nil {
			t.Fatalf("sign failed: %v", err)
		}
		groupPubCompressed := privKey.PubKey().SerializeCompressed()

		committee := []session.Participant{
			{ID: "a", Index: 0},
			{ID: "b", Index: 1},
			{ID: "c", Index: 2},
		}
		sess := session.NewSession(session.SessionParams{
			JobID:     "verify_basic",
			VaultID:   1,
			Chain:     "btc",
			KeyEpoch:  1,
			SignAlgo:  int32(signAlgo),
			Messages:  [][]byte{msg[:]},
			Committee: committee,
			Threshold: 2,
		})

		coordinator := newTestCoordinator(groupPubCompressed)
		valid, verifyErr, panicVal := coordinator.verifyAggregatedSignature(sess, 0, msg[:], sig.Serialize())
		if !valid || verifyErr != nil || panicVal != nil {
			t.Fatalf("expected verify success, got valid=%t err=%v panic=%v", valid, verifyErr, panicVal)
		}

		badSig := make([]byte, 64)
		valid, verifyErr, panicVal = coordinator.verifyAggregatedSignature(sess, 0, msg[:], badSig)
		if valid || verifyErr != nil || panicVal == nil {
			t.Fatalf("expected panic-only invalid path for bad signature, got valid=%t err=%v panic=%v", valid, verifyErr, panicVal)
		}
	})

	t.Run("replay_runtime_vector_from_session_dump", func(t *testing.T) {
		groupPubCompressed := mustHex(t, "03c629ebd4547a07d6652e8f8fc5156d0a6b95bfcc7906b8ff1bbb53c4af4bc716")
		msgVector := mustHex(t, "9d8aa12f8d5acaa5d97db30171a9078fcde6ed06c84cfaa8e2008954d8076ede")
		sigVector := mustHex(t, "87ac99196a15c5f991744059614769eba80b3fa3cfdcaa07ea36237eebe9b21ecbd92513ee40e5262d3a62a9cd1d05aff24ae083e9070bc5d10e4b45adc8d59a")

		hidingHex := []string{
			"024965b3a521a41e537595e0ae6295cdd2e22ace664483b9ffd891eb2b565886c3",
			"0372e386da8e84b3b32a9d9ece2aeda832de5b0edf92ab390c659487838a57208e",
			"033a34679dea7696d0cc7f8e73744a06dfde0443ba01d863472bfa051a413a8330",
			"0307e4a378273ee483abd591857b09cbb6778473d386dec9ae06bf8812a20c0e1c",
		}
		bindingHex := []string{
			"02c840ecca0834a82ba699d40a206e00d0f4603a2dd86774375e799aabbaf0038d",
			"036bb6ee72583ea7ecf53c8a58fe3e66d75453a4fb138357b584ddafaceb21c722",
			"02567b88767159b15828ca006fe93d63f1d2d6094f55385c5163b2feceed9050af",
			"035cc67cc919f4bc3b9af0cf26544fee357ff81cdc61077990c3734173ace9b2e5",
		}
		sharesHex := []string{
			"b55f0a618ee2bbe3cffb99d9a16e589e80c11858e58573188cd595870e8030a4",
			"24eb959eb83da6e7810b7e015343b84383e95796a0b066436764f84599d92250",
			"4c81bb000683d951917bae0027f5d05e147b98e6550b3aa91df616bfbbdb47f7",
			"a50cca13a09ca9094ab79cceb075246e93d3b494bd0e97fc7eb0054619ca7bf0",
		}

		sessVector := session.NewSession(session.SessionParams{
			JobID:    "1d42377df39e74643fd12222d2161d18cd5e1bc1a93ea63c1a88453fc936e289",
			VaultID:  0,
			Chain:    "btc",
			KeyEpoch: 1,
			SignAlgo: int32(signAlgo),
			Messages: [][]byte{msgVector},
			Committee: []session.Participant{
				{ID: "bc1qjx6cr60m07yjjrtpdkys84hnxtcjav8q52qy86", Index: 0},
				{ID: "bc1qg29pz4ec8tdfzt7p734u4k9l70nfp762pjg0n4", Index: 1},
				{ID: "bc1qqaw9tguaurp8t29cgu933ahlsn54xjjfqhwqyj", Index: 2},
				{ID: "bc1q3724u08886zrksw7j8axvclxy9kxew04j4l538", Index: 3},
			},
			Threshold: 3,
			MyIndex:   3,
		})
		if err := sessVector.Start(); err != nil {
			t.Fatalf("start session vector failed: %v", err)
		}
		sessVector.SelectedSet = []int{0, 1, 2, 3}
		for i := 0; i < 4; i++ {
			if err := sessVector.AddNonce(i, [][]byte{mustHex(t, hidingHex[i])}, [][]byte{mustHex(t, bindingHex[i])}); err != nil {
				t.Fatalf("add nonce[%d] failed: %v", i, err)
			}
		}
		sessVector.SetState(session.SignSessionStateCollectingShares)
		for i := 0; i < 4; i++ {
			if err := sessVector.AddShare(i, [][]byte{mustHex(t, sharesHex[i])}); err != nil {
				t.Fatalf("add share[%d] failed: %v", i, err)
			}
		}
		sessVector.SetState(session.SignSessionStateAggregating)

		vectorCoordinator := newTestCoordinator(groupPubCompressed)
		valid, verifyErr, panicVal := vectorCoordinator.verifyAggregatedSignature(sessVector, 0, msgVector, sigVector)
		if valid || verifyErr != nil || panicVal == nil {
			t.Fatalf("expected panic-only failure for runtime vector, got valid=%t err=%v panic=%v", valid, verifyErr, panicVal)
		}

		coreNonces := make([]coreRoast.SignerNonce, 0, 4)
		coreShares := make([]coreRoast.SignerShare, 0, 4)
		for i := 0; i < 4; i++ {
			hidingPoint, err := decodeSerializedPoint(signAlgo, mustHex(t, hidingHex[i]))
			if err != nil {
				t.Fatalf("decode hiding point[%d] failed: %v", i, err)
			}
			bindingPoint, err := decodeSerializedPoint(signAlgo, mustHex(t, bindingHex[i]))
			if err != nil {
				t.Fatalf("decode binding point[%d] failed: %v", i, err)
			}
			coreNonces = append(coreNonces, coreRoast.SignerNonce{
				SignerID:     i + 1,
				HidingPoint:  curve.Point(hidingPoint),
				BindingPoint: curve.Point(bindingPoint),
			})
			coreShares = append(coreShares, coreRoast.SignerShare{
				SignerID: i + 1,
				Share:    new(big.Int).SetBytes(mustHex(t, sharesHex[i])),
			})
		}
		R, err := coreRoast.ComputeGroupCommitment(coreNonces, msgVector, group)
		if err != nil {
			t.Fatalf("compute group commitment failed: %v", err)
		}
		if R.Y == nil || R.Y.Bit(0) != 0 {
			t.Fatalf("expected even group commitment parity for vector, got parity=%d", R.Y.Bit(0))
		}

		sigLegacy, err := coreRoast.AggregateSignatures(R, coreShares, group)
		if err != nil {
			t.Fatalf("aggregate legacy failed: %v", err)
		}
		sigBIP340, err := coreRoast.AggregateSignaturesBIP340(R, coreShares, group)
		if err != nil {
			t.Fatalf("aggregate bip340 failed: %v", err)
		}
		if !bytes.Equal(sigVector, sigLegacy) || !bytes.Equal(sigVector, sigBIP340) {
			t.Fatalf("vector signature does not match recomputed aggregation")
		}

		t.Logf("runtime vector reproduced: verify panic persists with even R.y and identical legacy/BIP340 aggregation; root cause is upstream (participant share/key mapping or share source)")
	})
}
