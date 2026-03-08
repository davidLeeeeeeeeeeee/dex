package roast

import (
	"fmt"
	"math/big"

	"dex/frost/core/curve"
	"dex/pb"
	"dex/utils"
)

func taskTweakAt(tweaks [][]byte, taskIdx int) []byte {
	if taskIdx < 0 || taskIdx >= len(tweaks) {
		return nil
	}
	return tweaks[taskIdx]
}

func deriveSigningPubkeyForTask(signAlgo pb.SignAlgo, groupPubkey, tweak []byte) ([]byte, *big.Int, error) {
	verifyPubkey := append([]byte(nil), groupPubkey...)

	if signAlgo != pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340 {
		groupPubX := big.NewInt(0)
		switch len(groupPubkey) {
		case 33:
			groupPubX.SetBytes(groupPubkey[1:33])
		default:
			if len(groupPubkey) < 32 {
				return nil, nil, fmt.Errorf("invalid group pubkey length: %d", len(groupPubkey))
			}
			groupPubX.SetBytes(groupPubkey[:32])
		}
		return verifyPubkey, groupPubX, nil
	}

	if len(tweak) == 32 {
		tweakedPubkey, err := utils.ComputeTweakedPubkey(groupPubkey, tweak)
		if err != nil {
			return nil, nil, fmt.Errorf("compute tweaked pubkey failed: %w", err)
		}
		verifyPubkey = tweakedPubkey
	}

	xOnly, err := utils.NormalizeSecp256k1XOnlyPubKey(verifyPubkey)
	if err != nil {
		return nil, nil, err
	}
	return xOnly, new(big.Int).SetBytes(xOnly), nil
}

func deriveTaskSecretShare(signAlgo pb.SignAlgo, groupPubkey, rawShare, tweak []byte) (*big.Int, bool, bool, error) {
	share := new(big.Int).SetBytes(rawShare)
	if signAlgo != pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340 {
		return share, false, false, nil
	}

	// Step 1: normalize against the internal key's x-only even-Y lift.
	shareNegated := false
	share = normalizeSecretShareForBIP340(signAlgo, groupPubkey, share)
	if share.Cmp(new(big.Int).SetBytes(rawShare)) != 0 {
		shareNegated = true
	}

	if len(tweak) != 32 {
		return share, shareNegated, false, nil
	}

	// Step 2: add tweak to the normalized internal key share.
	order := curve.NewSecp256k1Group().Order()
	tweakScalar := new(big.Int).SetBytes(tweak)
	tweakScalar.Mod(tweakScalar, order)
	share = new(big.Int).Add(share, tweakScalar)
	share.Mod(share, order)

	// Step 3: normalize again against the final tweaked signing pubkey's
	// x-only even-Y lift. Taproot can flip parity after adding the tweak.
	tweakedPubkey, err := utils.ComputeTweakedPubkey(groupPubkey, tweak)
	if err != nil {
		return nil, false, false, fmt.Errorf("compute tweaked pubkey for task share failed: %w", err)
	}
	normalizedTweakedShare := normalizeSecretShareForBIP340(signAlgo, tweakedPubkey, share)
	if normalizedTweakedShare.Cmp(share) != 0 {
		shareNegated = true
	}
	return normalizedTweakedShare, shareNegated, true, nil
}
