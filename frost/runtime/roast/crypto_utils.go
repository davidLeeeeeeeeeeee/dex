package roast

import (
	"fmt"

	"dex/frost/core/curve"
	"dex/pb"
)

// getPointSize returns serialized point length for the sign algorithm.
func getPointSize(algo pb.SignAlgo) int {
	switch algo {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		return 33 // compressed secp256k1 pubkey
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128:
		return 64 // X || Y
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		return 32 // compressed Edwards point
	default:
		return 32
	}
}

func decodeSerializedPoint(algo pb.SignAlgo, data []byte) (CurvePoint, error) {
	var p curve.Point

	switch algo {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		if len(data) != 33 {
			return CurvePoint{}, fmt.Errorf("invalid secp256k1 point length: %d", len(data))
		}
		p = curve.NewSecp256k1Group().DecompressPoint(data)
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128:
		if len(data) != 64 {
			return CurvePoint{}, fmt.Errorf("invalid bn128 point length: %d", len(data))
		}
		p = curve.NewBN256Group().DecompressPoint(data)
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		if len(data) != 32 {
			return CurvePoint{}, fmt.Errorf("invalid ed25519 point length: %d", len(data))
		}
		p = curve.NewEd25519Group().DecompressPoint(data)
	default:
		return CurvePoint{}, fmt.Errorf("unsupported sign algo for point decode: %s", algo.String())
	}

	if p.X == nil || p.Y == nil {
		return CurvePoint{}, fmt.Errorf("failed to decode point for algo=%s", algo.String())
	}
	return CurvePoint{X: p.X, Y: p.Y}, nil
}
