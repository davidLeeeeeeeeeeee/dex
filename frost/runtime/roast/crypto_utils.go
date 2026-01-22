package roast

import "dex/pb"

// getPointSize 返回指定签名算法的曲线点序列化长度
func getPointSize(algo pb.SignAlgo) int {
	switch algo {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		return 33 // 压缩格式 (0x02/0x03 + X)
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128:
		return 64 // X || Y (32 + 32)
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		return 32 // 标准 Ed25519 压缩格式
	default:
		return 32 // 默认回退
	}
}
