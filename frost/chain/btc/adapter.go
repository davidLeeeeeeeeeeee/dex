// frost/chain/btc/adapter.go
// BTC 閾鹃€傞厤鍣細鏋勫缓 Taproot 浜ゆ槗妯℃澘銆佽绠?sighash銆佺粍瑁呯鍚嶄氦鏄?

package btc

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"dex/frost/chain"
	"dex/pb"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// ========== 甯搁噺 ==========

const (
	// TaprootWitnessVersion Taproot witness 鐗堟湰
	TaprootWitnessVersion = 0x01
	// SchnorrSigSize Schnorr 绛惧悕澶у皬锛?4 瀛楄妭锛?
	SchnorrSigSize = 64
)

// ========== 閿欒瀹氫箟 ==========

var (
	// ErrInvalidTemplateData 鏃犳晥鐨勬ā鏉挎暟鎹?
	ErrInvalidTemplateData = errors.New("invalid template data")
	// ErrInvalidSignature 鏃犳晥鐨勭鍚?
	ErrInvalidSignature = errors.New("invalid signature")
	// ErrSignatureCountMismatch 绛惧悕鏁伴噺涓嶅尮閰?
	ErrSignatureCountMismatch = errors.New("signature count mismatch with inputs")
)

// ========== BTCAdapter ==========

// BTCAdapter BTC 閾鹃€傞厤鍣?
type BTCAdapter struct {
	// 缃戠粶鍙傛暟锛坢ainnet/testnet/regtest锛?
	network string
}

// NewBTCAdapter 鍒涘缓 BTC 閫傞厤鍣?
func NewBTCAdapter(network string) *BTCAdapter {
	if network == "" {
		network = "mainnet"
	}
	return &BTCAdapter{
		network: network,
	}
}

// Chain 杩斿洖閾炬爣璇?
func (a *BTCAdapter) Chain() string {
	return chain.ChainBTC
}

// SignAlgo 杩斿洖绛惧悕绠楁硶
func (a *BTCAdapter) SignAlgo() pb.SignAlgo {
	return chain.SignAlgoBTC
}

// BuildWithdrawTemplate 鏋勫缓鎻愮幇浜ゆ槗妯℃澘
func (a *BTCAdapter) BuildWithdrawTemplate(params chain.WithdrawTemplateParams) (*chain.TemplateResult, error) {
	// 鍒涘缓 BTC 妯℃澘
	template, err := NewBTCTemplate(params)
	if err != nil {
		return nil, err
	}

	// 璁＄畻妯℃澘鍝堝笇
	templateHash := template.TemplateHash()

	// 搴忓垪鍖栨ā鏉挎暟鎹?
	templateData, err := template.ToJSON()
	if err != nil {
		return nil, err
	}

	// 鏋勫缓姣忎釜 input 鐨?scriptPubKey锛堜粠 UTXO 鑾峰彇锛?
	scriptPubKeys := make([][]byte, len(params.Inputs))
	for i, utxo := range params.Inputs {
		scriptPubKeys[i] = utxo.ScriptPubKey
	}

	// 璁＄畻 Taproot sighash
	sighashes, err := template.ComputeTaprootSighash(scriptPubKeys, SighashDefault)
	if err != nil {
		return nil, err
	}

	return &chain.TemplateResult{
		TemplateHash: templateHash,
		TemplateData: templateData,
		SigHashes:    sighashes,
	}, nil
}

// TemplateHash 璁＄畻妯℃澘鍝堝笇
func (a *BTCAdapter) TemplateHash(templateData []byte) ([]byte, error) {
	template, err := FromJSON(templateData)
	if err != nil {
		return nil, ErrInvalidTemplateData
	}
	return template.TemplateHash(), nil
}

// PackageSigned 缁勮绛惧悕鍚庣殑浜ゆ槗鍖?
func (a *BTCAdapter) PackageSigned(templateData []byte, signatures [][]byte) (*chain.SignedPackage, error) {
	template, err := FromJSON(templateData)
	if err != nil {
		return nil, ErrInvalidTemplateData
	}

	// 楠岃瘉绛惧悕鏁伴噺
	if len(signatures) != len(template.Inputs) {
		return nil, ErrSignatureCountMismatch
	}

	// 楠岃瘉绛惧悕鏍煎紡
	for _, sig := range signatures {
		if len(sig) != SchnorrSigSize {
			return nil, ErrInvalidSignature
		}
	}

	// 鏋勫缓瀹屾暣浜ゆ槗
	rawTx, err := a.buildRawTransaction(template, signatures)
	if err != nil {
		return nil, err
	}

	return &chain.SignedPackage{
		TemplateHash: template.TemplateHash(),
		TemplateData: templateData,
		Signatures:   signatures,
		RawTx:        rawTx,
	}, nil
}

// buildRawTransaction 鏋勫缓鍘熷浜ゆ槗
func (a *BTCAdapter) buildRawTransaction(template *BTCTemplate, signatures [][]byte) ([]byte, error) {
	var buf bytes.Buffer

	// 1. 鐗堟湰鍙凤紙4 bytes, little-endian锛?
	binary.Write(&buf, binary.LittleEndian, template.Version)

	// 2. Marker + Flag锛圫egWit锛?
	buf.WriteByte(0x00) // marker
	buf.WriteByte(0x01) // flag

	// 3. 杈撳叆鏁伴噺锛坴arint锛?
	writeVarInt(&buf, uint64(len(template.Inputs)))

	// 4. 杈撳叆鍒楄〃
	for _, in := range template.Inputs {
		// txid锛?2 bytes锛屽弽杞负鍐呴儴瀛楄妭搴忥級
		txidBytes, _ := hex.DecodeString(in.TxID)
		reverseBytes(txidBytes)
		buf.Write(txidBytes)
		// vout锛? bytes锛?
		binary.Write(&buf, binary.LittleEndian, in.Vout)
		// scriptSig 闀垮害锛? for SegWit锛?
		buf.WriteByte(0x00)
		// sequence锛? bytes锛?
		binary.Write(&buf, binary.LittleEndian, in.Sequence)
	}

	// 5. 杈撳嚭鏁伴噺锛坴arint锛?
	writeVarInt(&buf, uint64(len(template.Outputs)))

	// 6. 杈撳嚭鍒楄〃
	for _, out := range template.Outputs {
		// amount锛? bytes锛?
		binary.Write(&buf, binary.LittleEndian, out.Amount)
		// scriptPubKey
		scriptPubKey := a.addressToScriptPubKey(out.Address)
		writeVarInt(&buf, uint64(len(scriptPubKey)))
		buf.Write(scriptPubKey)
	}

	// 7. Witness 鏁版嵁
	for _, sig := range signatures {
		// witness 鍏冪礌鏁伴噺锛? for key-path spend锛?
		writeVarInt(&buf, 1)
		// 绛惧悕闀垮害 + 绛惧悕
		writeVarInt(&buf, uint64(len(sig)))
		buf.Write(sig)
	}

	// 8. 閿佸畾鏃堕棿锛? bytes锛?
	binary.Write(&buf, binary.LittleEndian, template.LockTime)

	return buf.Bytes(), nil
}

// addressToScriptPubKey 灏嗗湴鍧€杞崲涓?scriptPubKey
// 鏀寔 Taproot (bc1p...) 鍜?Native SegWit (bc1q...) 鍦板潃
func (a *BTCAdapter) addressToScriptPubKey(address string) []byte {
	// 灏濊瘯瑙ｆ瀽 Taproot 鍦板潃 (bech32m, bc1p...)
	if len(address) > 4 && (address[:4] == "bc1p" || address[:4] == "tb1p") {
		// Taproot 鍦板潃
		decoded, err := decodeBech32m(address)
		if err == nil && len(decoded) == 32 {
			// Taproot scriptPubKey: OP_1 <32-byte-pubkey>
			scriptPubKey := make([]byte, 34)
			scriptPubKey[0] = 0x51 // OP_1
			scriptPubKey[1] = 0x20 // 32 bytes
			copy(scriptPubKey[2:], decoded)
			return scriptPubKey
		}
	}

	// 灏濊瘯瑙ｆ瀽 Native SegWit 鍦板潃 (bech32, bc1q...)
	if len(address) > 4 && (address[:4] == "bc1q" || address[:4] == "tb1q") {
		decoded, err := decodeBech32(address)
		if err == nil && len(decoded) == 20 {
			// P2WPKH scriptPubKey: OP_0 <20-byte-hash>
			scriptPubKey := make([]byte, 22)
			scriptPubKey[0] = 0x00 // OP_0
			scriptPubKey[1] = 0x14 // 20 bytes
			copy(scriptPubKey[2:], decoded)
			return scriptPubKey
		}
	}

	// 鍥為€€锛氫娇鐢ㄥ湴鍧€鐨?hash 浣滀负 Taproot pubkey
	scriptPubKey := make([]byte, 34)
	scriptPubKey[0] = 0x51 // OP_1
	scriptPubKey[1] = 0x20 // 32 bytes
	hash := sha256.Sum256([]byte(address))
	copy(scriptPubKey[2:], hash[:])
	return scriptPubKey
}

// VerifySignature 楠岃瘉 BIP-340 Schnorr 绛惧悕
func (a *BTCAdapter) VerifySignature(groupPubkey []byte, msg []byte, signature []byte) (bool, error) {
	if len(signature) != SchnorrSigSize {
		return false, ErrInvalidSignature
	}
	xOnlyPubkey, err := normalizeBIP340XOnlyPubKey(groupPubkey)
	if err != nil {
		return false, err
	}
	if len(msg) != 32 {
		return false, errors.New("invalid message length")
	}

	// 使用 btcec 库验证 BIP-340 Schnorr 签名
	pubKey, err := schnorr.ParsePubKey(xOnlyPubkey)
	if err != nil {
		return false, fmt.Errorf("parse pubkey: %w", err)
	}

	sig, err := schnorr.ParseSignature(signature)
	if err != nil {
		return false, fmt.Errorf("parse signature: %w", err)
	}

	return sig.Verify(msg, pubKey), nil
}

func normalizeBIP340XOnlyPubKey(pubKey []byte) ([]byte, error) {
	switch len(pubKey) {
	case 32:
		xOnly := make([]byte, 32)
		copy(xOnly, pubKey)
		return xOnly, nil
	case 33:
		if pubKey[0] != 0x02 && pubKey[0] != 0x03 {
			return nil, errors.New("invalid compressed pubkey prefix")
		}
		xOnly := make([]byte, 32)
		copy(xOnly, pubKey[1:])
		return xOnly, nil
	default:
		return nil, errors.New("invalid pubkey length")
	}
}

// ========== 杈呭姪鍑芥暟 ==========

// ComputeTxID 璁＄畻浜ゆ槗 ID
func ComputeTxID(rawTx []byte) []byte {
	// 瀵逛簬 SegWit 浜ゆ槗锛宼xid 鏄笉鍖呭惈 witness 鏁版嵁鐨勪氦鏄撶殑鍙?SHA256
	// 杩欓噷绠€鍖栧鐞?
	hash1 := sha256.Sum256(rawTx)
	hash2 := sha256.Sum256(hash1[:])
	return hash2[:]
}

// ========== Bech32/Bech32m 瑙ｇ爜 ==========

// bech32 瀛楃闆?
const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// decodeBech32 瑙ｇ爜 bech32 鍦板潃锛堢敤浜?bc1q... 鍦板潃锛?
func decodeBech32(address string) ([]byte, error) {
	return decodeBech32Internal(address, false)
}

// decodeBech32m 瑙ｇ爜 bech32m 鍦板潃锛堢敤浜?bc1p... Taproot 鍦板潃锛?
func decodeBech32m(address string) ([]byte, error) {
	return decodeBech32Internal(address, true)
}

// decodeBech32Internal 鍐呴儴 bech32/bech32m 瑙ｇ爜
func decodeBech32Internal(address string, isBech32m bool) ([]byte, error) {
	// 鎵惧埌鍒嗛殧绗?'1'
	sepIdx := -1
	for i := len(address) - 1; i >= 0; i-- {
		if address[i] == '1' {
			sepIdx = i
			break
		}
	}
	if sepIdx < 1 || sepIdx+7 > len(address) {
		return nil, errors.New("invalid bech32 address")
	}

	// 瑙ｇ爜鏁版嵁閮ㄥ垎
	data := address[sepIdx+1:]
	values := make([]int, len(data))
	for i, c := range data {
		idx := -1
		for j, ch := range bech32Charset {
			if byte(ch) == byte(c) {
				idx = j
				break
			}
		}
		if idx == -1 {
			return nil, errors.New("invalid bech32 character")
		}
		values[i] = idx
	}

	// 楠岃瘉鏍￠獙鍜岋紙绠€鍖栵細璺宠繃鏍￠獙鍜岄獙璇侊級
	if len(values) < 6 {
		return nil, errors.New("bech32 data too short")
	}

	// 绉婚櫎鏍￠獙鍜岋紙鏈€鍚?6 涓瓧绗︼級
	values = values[:len(values)-6]

	// 绗竴涓€兼槸 witness 鐗堟湰
	if len(values) < 1 {
		return nil, errors.New("missing witness version")
	}
	// witnessVersion := values[0]
	values = values[1:]

	// 灏?5-bit 鍊艰浆鎹负 8-bit 瀛楄妭
	return convertBits(values, 5, 8, false)
}

// convertBits 鍦ㄤ笉鍚屼綅瀹戒箣闂磋浆鎹?
func convertBits(data []int, fromBits, toBits int, pad bool) ([]byte, error) {
	acc := 0
	bits := 0
	var result []byte
	maxv := (1 << toBits) - 1

	for _, value := range data {
		if value < 0 || value>>fromBits != 0 {
			return nil, errors.New("invalid value")
		}
		acc = (acc << fromBits) | value
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			result = append(result, byte((acc>>bits)&maxv))
		}
	}

	if pad {
		if bits > 0 {
			result = append(result, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, errors.New("invalid padding")
	}

	return result, nil
}
