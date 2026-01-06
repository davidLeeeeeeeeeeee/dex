// frost/runtime/interfaces.go
// 定义 Runtime 层与 Core 层之间的抽象接口，实现解耦

package runtime

import "dex/frost/runtime/types"

// ========== 通用类型 ==========

// CurvePoint 抽象椭圆曲线点（从 types 包导入）
type CurvePoint = types.CurvePoint

// ========== ROAST 相关接口 ==========

// NonceInput 签名者的 nonce 输入（从 types 包导入）
type NonceInput = types.NonceInput

// ShareInput 签名份额输入（从 types 包导入）
type ShareInput = types.ShareInput

// PartialSignParams 计算部分签名的参数（从 types 包导入）
type PartialSignParams = types.PartialSignParams

// ROASTExecutor 抽象 ROAST 密码学操作（从 types 包导入）
type ROASTExecutor = types.ROASTExecutor

// ========== DKG 相关接口 ==========

// PolynomialHandle 多项式句柄（从 types 包导入）
type PolynomialHandle = types.PolynomialHandle

// DKGExecutor 抽象 DKG 密码学操作（从 types 包导入）
type DKGExecutor = types.DKGExecutor

// ========== 工厂接口 ==========

// CryptoExecutorFactory 创建指定算法的密码学执行器（从 types 包导入）
type CryptoExecutorFactory = types.CryptoExecutorFactory
