package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/btcsuite/btcd/btcec/v2"
	"math/big"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/sha3"
)

var Port = ""

// DeriveEthereumAddress 模拟以太坊的地址推导: keccak256(pubUncompressed[1:])最后20字节
func DeriveEthereumAddress(privKey *secp256k1.PrivateKey) string {
	// 先获取 uncompressed 公钥 (首字节0x04 + 32字节X + 32字节Y = 65字节)
	pubUncompressed := privKey.PubKey().SerializeUncompressed()

	// keccak-256
	hash := sha3.NewLegacyKeccak256()
	// 跳过首字节 0x04，剩余 64 字节是 X、Y
	hash.Write(pubUncompressed[1:])
	digest := hash.Sum(nil) // 32字节

	// 取后20字节作为地址
	addr := digest[12:] // 最后20字节
	return "0x" + hex.EncodeToString(addr)
}

// DeriveBtcBech32Address 使用 btcsuite/btcd 库生成 bc1q 地址
func DeriveBtcBech32Address(privKey *secp256k1.PrivateKey) (string, error) {
	// 1. 从私钥得到压缩公钥哈希 (Hash160 == SHA-256 + RIPEMD-160)
	pubKeyHash := btcutil.Hash160(privKey.PubKey().SerializeCompressed())

	// 2. 使用 btcutil.NewAddressWitnessPubKeyHash 生成 P2WPKH 地址
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, &chaincfg.MainNetParams)
	if err != nil {
		return "", err
	}

	return addr.String(), nil
} // 从私钥生成 bc1q… P2WPKH 地址

// DeriveBtcTaprootAddress 接受 secp256k1 私钥，返回 BIP-341 Taproot (bc1p…) 地址
func DeriveBtcTaprootAddress(privKey *btcec.PrivateKey) (string, error) {
	curve := btcec.S256()

	// 1. 内部公钥 P
	P := privKey.PubKey()
	// x-only 序列化（剥去压缩格式首字节 0x02/0x03）
	comp := P.SerializeCompressed()
	xOnly := comp[1:]

	// 2. 计算 TapTweak：tagHash = SHA256("TapTweak")
	tagHash := sha256.Sum256([]byte("TapTweak"))
	h := sha256.New()
	h.Write(tagHash[:])
	h.Write(tagHash[:])
	h.Write(xOnly) // 只用内部公钥的 x 坐标
	tweakBytes := h.Sum(nil)

	// 3. tweak mod n
	n := curve.Params().N
	tweak := new(big.Int).SetBytes(tweakBytes)
	tweak.Mod(tweak, n)

	// 4. 输出公钥 Q = P + tweak·G
	tPx, tPy := curve.ScalarBaseMult(tweak.Bytes())
	Qx, _ := curve.Add(P.X(), P.Y(), tPx, tPy)

	// 5. 序列化 Q 的 x-only
	xOnlyOut := make([]byte, 32)
	bx := Qx.Bytes()
	copy(xOnlyOut[32-len(bx):], bx)

	// 6. 构造 Bech32m v1 Taproot 地址
	addr, err := btcutil.NewAddressTaproot(xOnlyOut, &chaincfg.MainNetParams)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

// ParseSecp256k1PrivateKey 同时支持 WIF 或 16 进制的32字节私钥字符串
func ParseSecp256k1PrivateKey(keyStr string) (*secp256k1.PrivateKey, error) {
	// 1) 尝试当作WIF解析
	if wif, err := btcutil.DecodeWIF(keyStr); err == nil {
		// 解析成功，直接返回其内部的 *secp256k1.privateKey
		return wif.PrivKey, nil
	}

	// 2) 如果不是WIF，则尝试按Hex进行解析
	raw, err := hex.DecodeString(keyStr)
	if err != nil {
		return nil, errors.New("invalid key (neither valid WIF nor valid hex): " + err.Error())
	}
	if len(raw) != 32 {
		return nil, errors.New("invalid private key length in hex (must be 32 bytes)")
	}

	// 3) 使用 32 字节原生私钥
	privKey := secp256k1.PrivKeyFromBytes(raw)
	return privKey, nil
}

// ParseECDSAPrivateKey 支持WIF和16进制私钥字符串，返回标准库的 *ecdsa.PrivateKey
func ParseECDSAPrivateKey(priKey string) (*ecdsa.PrivateKey, error) {
	// 将 priKey 从十六进制字符串解析为 big.Int
	d := new(big.Int)
	_, ok := d.SetString(priKey, 16)
	if !ok {
		return nil, errors.New("无效的私钥格式")
	}

	// 选择适当的椭圆曲线，根据实际需求选择，例如 secp256k1 或 elliptic.P256()
	// 此处以 P256 为例
	curve := elliptic.P256()

	// 使用私钥 D 计算公钥 (X, Y) = D * G
	x, y := curve.ScalarBaseMult(d.Bytes())

	// 构造并返回完整的 ecdsa.PrivateKey 对象
	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: curve,
			X:     x,
			Y:     y,
		},
		D: d,
	}

	return priv, nil
}

// GeneratePairKey 根据 base_token 和 quote_token 生成“交易对key”
func GeneratePairKey(baseToken, quoteToken string) string {
	if baseToken < quoteToken {
		return baseToken + "_" + quoteToken
	}
	return quoteToken + "_" + baseToken
}
