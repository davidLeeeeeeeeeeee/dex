package main

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

type SimpleFetcher struct {
	PrevOuts map[wire.OutPoint]*wire.TxOut
}

func (s *SimpleFetcher) FetchPrevOutput(op wire.OutPoint) *wire.TxOut {
	return s.PrevOuts[op]
}

func main() {
	// 私钥（WIF格式）
	wifStr := "cNLr6L7gk9ecnhfPFPSzoBTJrjiDDDwt4xx7RdvtK4PBGzXuumUZ"
	wif, err := btcutil.DecodeWIF(wifStr)
	if err != nil {
		panic(err)
	}

	netParams := &chaincfg.TestNet3Params

	// UTXO 信息
	utxos := []struct {
		TxID     string
		Vout     uint32
		Value    int64
		PkScript string
	}{
		{"affe2f9b98477a73d6b85ece77c331b3750a279a96ea3a5f8cb3f0fac24870af", 0, 7750, "5120685bed648b6e1ae9ef4ae70cccd5f180c0f6af1f83f92a9cee94895157e9daa9"},
	}

	tx := wire.NewMsgTx(wire.TxVersion)

	// 构建输入
	fetcher := &SimpleFetcher{PrevOuts: make(map[wire.OutPoint]*wire.TxOut)}
	for _, u := range utxos {
		hash, err := chainhash.NewHashFromStr(u.TxID)
		if err != nil {
			panic(err)
		}
		op := wire.OutPoint{Hash: *hash, Index: u.Vout}
		tx.AddTxIn(wire.NewTxIn(&op, nil, nil))

		pkScript, err := hex.DecodeString(u.PkScript)
		if err != nil {
			panic(err)
		}

		fetcher.PrevOuts[op] = &wire.TxOut{
			Value:    u.Value,
			PkScript: pkScript,
		}
	}

	// 构建输出
	toAddr, _ := btcutil.DecodeAddress("tb1pdpd76eytdcdwnm62uuxve403srq0dtcls0uj488wjjy4z4lfm25sdwatf0", netParams)
	sendAmt := int64(7750)
	toScript, _ := txscript.PayToAddrScript(toAddr)
	tx.AddTxOut(wire.NewTxOut(sendAmt, toScript))

	// 找零与手续费
	fee := int64(250)
	var totalIn int64
	for _, u := range utxos {
		totalIn += u.Value
	}
	changeAmt := totalIn - sendAmt - fee
	if changeAmt > 0 {
		changeAddr, _ := btcutil.DecodeAddress("tb1pegupuqdwptus53zrs44mfjlm0ea5uxs46pugqulxurafw3jclp8q74sxd0", netParams)
		changeScript, _ := txscript.PayToAddrScript(changeAddr)
		tx.AddTxOut(wire.NewTxOut(changeAmt, changeScript))
	}

	// 签名
	sigHashes := txscript.NewTxSigHashes(tx, fetcher)
	for i := range tx.TxIn {
		sigHash, err := txscript.CalcTaprootSignatureHash(
			sigHashes,
			txscript.SigHashDefault,
			tx,
			i,
			fetcher,
		)
		if err != nil {
			panic(err)
		}

		schnorrSig, err := schnorr.Sign(wif.PrivKey, sigHash)
		if err != nil {
			panic(err)
		}

		tx.TxIn[i].Witness = wire.TxWitness{schnorrSig.Serialize()}
	}

	// 输出交易
	var buf bytes.Buffer
	tx.Serialize(&buf)
	fmt.Printf("Raw TX: %s\n", hex.EncodeToString(buf.Bytes()))
}
