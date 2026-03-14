// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

library AltBn128 {
    // 曲线质数 (field prime)
    uint256 internal constant P =
    21888242871839275222246405745257275088696311157297823662689037894645226208583;

    // 组阶 (group order)
    uint256 internal constant N =
    21888242871839275222246405745257275088548364400416034343698204186575808495617;

    struct G1Point {
        uint256 x;
        uint256 y;
    }

    /// @dev 预编译 0x06: 点加法
    function ecAdd(G1Point memory p, G1Point memory q)
    internal view
    returns (G1Point memory r)
    {
        uint256[4] memory input = [p.x, p.y, q.x, q.y];
        bool success;
        assembly {
        // call ecAdd precompile
        // input: 0x80, size: 0x80, output: 0x80, size: 0x40
            success := staticcall(gas(), 0x06, input, 0x80, r, 0x40)
        }
        require(success, "ecAdd failed");
    }

    /// @dev 预编译 0x07: 标量乘
    function ecMul(G1Point memory p, uint256 s)
    internal view
    returns (G1Point memory r)
    {
        uint256[3] memory input = [p.x, p.y, s];
        bool success;
        assembly {
        // call ecMul precompile
            success := staticcall(gas(), 0x07, input, 0x60, r, 0x40)
        }
        require(success, "ecMul failed");
    }

    /// @dev 点取反 (x, -y mod P)
    function negate(G1Point memory p) internal pure returns (G1Point memory) {
        if (p.x == 0 && p.y == 0) return G1Point(0, 0);
        return G1Point(p.x, P - (p.y % P));
    }

    // 生成元 G1 (1,2)
    function generator() internal pure returns (G1Point memory) {
        return G1Point(1, 2);
    }
}

library SchnorrBN128 {
    using AltBn128 for AltBn128.G1Point;

    /// @notice 验证单个 Schnorr 签名
    /// @param P      公钥 (Px, Py)
    /// @param R      随机点 (Rx, Ry)
    /// @param s      标量 s
    /// @param mHash  消息哈希（32字节）
    /// @return true  签名有效
    function verify(
        AltBn128.G1Point memory P,
        AltBn128.G1Point memory R,
        uint256 s,
        bytes32 mHash
    ) internal view returns (bool) {
        // 1. 输入合法性检查
        if (_isInf(R) || _isInf(P)) return false;
        if (s == 0 || s >= AltBn128.N) return false;

        // 2. 计算 e = H(R || P || m) mod n
        uint256 e = uint256(
            keccak256(
                abi.encodePacked(R.x, R.y, P.x, P.y, mHash)
            )
        ) % AltBn128.N;

        // 3. 计算:  s*G - e*P
        AltBn128.G1Point memory sG   = AltBn128.ecMul(AltBn128.generator(), s);
        AltBn128.G1Point memory eP   = AltBn128.ecMul(P, e);
        AltBn128.G1Point memory rhs  = AltBn128.ecAdd(sG, AltBn128.negate(eP));

        // 4. 比较是否与 R 相等
        return (rhs.x == R.x && rhs.y == R.y);
    }

    /// @notice 批量验证（线性组合技巧）
    /// @dev    随机系数 a_i 由调用者生成并传入，可使用链下随机或 keccak 链上确定性随机
    /// @param  a      随机标量数组，与签名数组一一对应
    /// @return true   全部通过，否则 false
    function verifyBatch(
        AltBn128.G1Point[] memory P,  // 公钥列表
        AltBn128.G1Point[] memory R,  // R 列表
        uint256[]         memory s,   // s 列表
        bytes32[]         memory mHash, // 消息哈希
        uint256[]         memory a    // 随机系数
    ) internal view returns (bool)
    {
        uint256 len = P.length;
        require(
            R.length == len && s.length == len && mHash.length == len && a.length == len,
            "array length mismatch"
        );

        // 累加 lhs = Σ a_i * s_i,   rhsPoints = Σ a_i * (R_i + e_i * P_i)
        uint256 lhsScalar = 0;
        AltBn128.G1Point memory rhsAccum = AltBn128.G1Point(0, 0);

        for (uint256 i; i < len; ++i) {
            uint256 e = uint256(
                keccak256(
                    abi.encodePacked(R[i].x, R[i].y, P[i].x, P[i].y, mHash[i])
                )
            ) % AltBn128.N;

            // lhs 标量累加
            lhsScalar = addmod(lhsScalar, mulmod(a[i], s[i], AltBn128.N), AltBn128.N);

            // rhs 点累加:   a_i * R_i + a_i * e_i * P_i
            AltBn128.G1Point memory term1 = AltBn128.ecMul(R[i], a[i]);
            AltBn128.G1Point memory term2 = AltBn128.ecMul(P[i], mulmod(a[i], e, AltBn128.N));

            rhsAccum = AltBn128.ecAdd(rhsAccum, term1);
            rhsAccum = AltBn128.ecAdd(rhsAccum, term2);
        }

        // 比较: lhs * G == rhsAccum
        AltBn128.G1Point memory lhsPoint = AltBn128.ecMul(AltBn128.generator(), lhsScalar);
        return (lhsPoint.x == rhsAccum.x && lhsPoint.y == rhsAccum.y);
    }

    // -------------------------------------------------------------------------
    // 内部工具
    // -------------------------------------------------------------------------
    function _isInf(AltBn128.G1Point memory p) private pure returns (bool) {
        return (p.x == 0 && p.y == 0);
    }
}

interface IERC20 {
    function transfer(address to, uint256 amount) external returns (bool);
}

contract SchnorrExample {
    using AltBn128    for AltBn128.G1Point;
    using SchnorrBN128 for AltBn128.G1Point;

    /// @notice 当前“授权”公钥 P
    AltBn128.G1Point public P;

    /// @notice 防止双重消费：message 的哈希 => 是否已消费
    mapping(bytes32 => bool) public consumed;

    event PubChanged(uint256 oldPx, uint256 oldPy, uint256 newPx, uint256 newPy);
    event Withdrawn(address indexed to, address indexed token, uint256 amount, bytes32 indexed hash);

    /// @dev 在链上初始化公钥 P
    constructor(uint256 Px, uint256 Py) {
        P = AltBn128.G1Point(Px, Py);
    }

    /// @notice 验证针对任意消息哈希的 Schnorr 签名
    function verifySignature(
        uint256 Px,
        uint256 Py,
        uint256 Rx,
        uint256 Ry,
        uint256 s,
        bytes32 messageHash
    ) public view returns (bool) {
        AltBn128.G1Point memory _P = AltBn128.G1Point(Px, Py);
        AltBn128.G1Point memory _R = AltBn128.G1Point(Rx, Ry);
        return SchnorrBN128.verify(_P, _R, s, messageHash);
    }

    /// @notice 根据链下签名指令提取 ETH/ERC20
    /// @param message    ABI 编码的 `(uint256 amount, address token, address to)`
    /// @param Rx, Ry, s  对 `keccak256(message)` 使用存储的 P 签名得到的 Schnorr 签名
    function withdraw(
        bytes calldata message,
        uint256 Rx,
        uint256 Ry,
        uint256 s
    ) external {
        // 1. 重放攻击检查
        bytes32 h = keccak256(message);
        require(!consumed[h], "SchnorrExample: already consumed");
        // 2. 在当前 P 下验证签名
        bool isa = verifySignature(P.x, P.y, Rx, Ry, s, h);
        require(isa, "SchnorrExample");

        // 3. 标记为已消费
        consumed[h] = true;
        // 4. 解码并执行转账
        (uint256 amount, address token, address to) = abi.decode(message, (uint256, address, address));
        if (token == address(0)) {
            // ETH
            payable(to).transfer(amount);
        } else {
            // ERC20
            require(IERC20(token).transfer(to, amount), "SchnorrExample: erc20 transfer failed");
        }
        emit Withdrawn(to, token, amount, h);
    }

    /// @notice 更改合约中存储的公钥 P
    /// @param newPx, newPy  要设置的新公钥坐标
    /// @param Rx, Ry, s     对 `keccak256(abi.encodePacked(newPx,newPy))` 使用旧 P 签名得到的 Schnorr 签名
    function changePub(
        uint256 newPx,
        uint256 newPy,
        uint256 Rx,
        uint256 Ry,
        uint256 s
    ) external {
        // 1. 构造消息哈希 = keccak256(newPx || newPy)
        bytes32 hPub = keccak256(abi.encodePacked(newPx, newPy));
        // 2. 在*当前* P 下验证签名有效性
        require(verifySignature(P.x, P.y, Rx, Ry, s, hPub),"SchnorrExample: invalid pubupdate signature");
        // 3. 原子性更新 P
        uint256 oldPx = P.x;
        uint256 oldPy = P.y;
        P = AltBn128.G1Point(newPx, newPy);
        emit PubChanged(oldPx, oldPy, newPx, newPy);
    }

    // 回退函数以接受 ETH
    receive() external payable {}
    fallback() external payable {}
}