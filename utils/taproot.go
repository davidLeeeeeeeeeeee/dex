package utils

import (
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

func NormalizeSecp256k1XOnlyPubKey(pubKey []byte) ([]byte, error) {
	switch len(pubKey) {
	case 32:
		return append([]byte(nil), pubKey...), nil
	case 33:
		if pubKey[0] != 0x02 && pubKey[0] != 0x03 {
			return nil, fmt.Errorf("invalid compressed pubkey prefix: 0x%x", pubKey[0])
		}
		return append([]byte(nil), pubKey[1:]...), nil
	default:
		return nil, fmt.Errorf("unsupported pubkey length: %d", len(pubKey))
	}
}

func BuildTaprootScriptPubKeyFromXOnly(xOnly []byte) ([]byte, error) {
	if len(xOnly) != 32 {
		return nil, fmt.Errorf("invalid taproot x-only pubkey length: %d", len(xOnly))
	}
	scriptPubKey := make([]byte, 34)
	scriptPubKey[0] = 0x51
	scriptPubKey[1] = 0x20
	copy(scriptPubKey[2:], xOnly)
	return scriptPubKey, nil
}

func BuildTaprootScriptPubKeyFromPubKey(pubKey []byte) ([]byte, error) {
	xOnly, err := NormalizeSecp256k1XOnlyPubKey(pubKey)
	if err != nil {
		return nil, err
	}
	return BuildTaprootScriptPubKeyFromXOnly(xOnly)
}

func ParseTaprootScriptPubKeyXOnly(scriptPubKey []byte) ([]byte, error) {
	if len(scriptPubKey) != 34 {
		return nil, fmt.Errorf("invalid taproot script_pubkey length: %d", len(scriptPubKey))
	}
	if scriptPubKey[0] != 0x51 || scriptPubKey[1] != 0x20 {
		return nil, fmt.Errorf("invalid taproot script_pubkey prefix: %x", scriptPubKey[:2])
	}
	return append([]byte(nil), scriptPubKey[2:]...), nil
}

func TaprootAddressFromXOnly(xOnly []byte, netParams *chaincfg.Params) (string, error) {
	addr, err := btcutil.NewAddressTaproot(xOnly, netParams)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

func TaprootAddressFromPubKey(pubKey []byte, netParams *chaincfg.Params) (string, error) {
	xOnly, err := NormalizeSecp256k1XOnlyPubKey(pubKey)
	if err != nil {
		return "", err
	}
	return TaprootAddressFromXOnly(xOnly, netParams)
}

func ComputeUserTweakForPubKey(groupPubkey []byte, userAddress string) ([]byte, error) {
	xOnly, err := NormalizeSecp256k1XOnlyPubKey(groupPubkey)
	if err != nil {
		return nil, err
	}
	return ComputeUserTweak(xOnly, userAddress), nil
}

func ComputeTweakedXOnlyPubkey(groupPubkey []byte, tweak []byte) ([]byte, error) {
	tweakedPubkey, err := ComputeTweakedPubkey(groupPubkey, tweak)
	if err != nil {
		return nil, err
	}
	return NormalizeSecp256k1XOnlyPubKey(tweakedPubkey)
}

func BuildTweakedTaprootScriptPubKey(groupPubkey []byte, receiverAddress string) ([]byte, error) {
	tweak, err := ComputeUserTweakForPubKey(groupPubkey, receiverAddress)
	if err != nil {
		return nil, err
	}
	tweakedXOnly, err := ComputeTweakedXOnlyPubkey(groupPubkey, tweak)
	if err != nil {
		return nil, err
	}
	return BuildTaprootScriptPubKeyFromXOnly(tweakedXOnly)
}
