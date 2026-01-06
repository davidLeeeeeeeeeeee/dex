// frost/runtime/workers/types.go
// 共享类型别名定义

package workers

import "dex/frost/runtime/types"

// 使用types包中定义的接口和类型
type StateReader = types.StateReader
type TxSubmitter = types.TxSubmitter
type VaultCommitteeProvider = types.VaultCommitteeProvider
type SignerInfo = types.SignerInfo
type MinerPubKeyProvider = types.MinerPubKeyProvider
type CryptoExecutorFactory = types.CryptoExecutorFactory
type ROASTExecutor = types.ROASTExecutor
type DKGExecutor = types.DKGExecutor
type PolynomialHandle = types.PolynomialHandle
type CurvePoint = types.CurvePoint
type NonceInput = types.NonceInput
type ShareInput = types.ShareInput
type PartialSignParams = types.PartialSignParams
