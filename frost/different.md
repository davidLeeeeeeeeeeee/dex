
原文https://www.btcstudy.org/2022/11/04/robust-asynchronous-schnorr-threshold-signature-tabconf-2022/
---

# Frost vs Roast — 本质区别说明

## 📌 1. 设计定位对比

```mermaid
flowchart LR
    A[FROST<br>Threshold Schnorr Signature] -->|作为签名算法| C[核心: 生成 t-of-n 门限签名]
    B[ROAST<br>Robust Asynchronous Wrapper] -->|作为协调层| D[核心: 保证签名最终能完成]

    C --> E[要求参与者在线 / 响应正常]
    D --> F[容忍网络异步 / 节点掉线 / 恶意节点]
```

---

## 📌 2. 基础结构对比（系统架构视角）

```mermaid
flowchart TD
    subgraph FROST
        F1[协调者 Coordinator]
        F2[Signer 1]
        F3[Signer 2]
        F4[Signer 3]
        F1 --> F2
        F1 --> F3
        F1 --> F4
        F2 --> F1
        F3 --> F1
        F4 --> F1
        FN[/"单轮失败 → 会话卡住<br>无 retry 机制<br>无 subset 切换"/]
    end

    subgraph ROAST
        R0[ROAST Orchestrator]
        R1[Signer A]
        R2[Signer B]
        R3[Signer C]
        R4[Signer D]
        R5[Signer E]

        R0 -->|尝试 1<br>子集: A,B,C| R1
        R0 --> R2
        R0 --> R3

        R0 -->|尝试 2<br>子集: B,D,E| R2
        R0 --> R4
        R0 --> R5

        R1 --> R0
        R2 --> R0
        R3 --> R0
        R4 --> R0
        R5 --> R0

        RN[/"可能进行多次 FROST 会话<br>自动跳过不响应节点<br>至少 t 个 honest 节点 → 成功"/]
    end
```

---

## 📌 3. 签名流程对比（时序图）

### 🔹 FROST 正常流程（两轮通信）

```mermaid
sequenceDiagram
    participant C as 协调者
    participant S1 as Signer 1
    participant S2 as Signer 2
    participant S3 as Signer 3

    Note over C,S3: Round 1: Nonce 承诺

    C->>S1: 广播 msg，请求 Nonce 承诺
    C->>S2: 广播 msg，请求 Nonce 承诺
    C->>S3: 广播 msg，请求 Nonce 承诺

    S1->>S1: 生成 k_1，计算 R_1 = k_1·G
    S2->>S2: 生成 k_2，计算 R_2 = k_2·G
    S3->>S3: 生成 k_3，计算 R_3 = k_3·G

    S1-->>C: 返回 R_1
    S2-->>C: 返回 R_2
    S3-->>C: 返回 R_3

    Note over C,S3: Round 2: 广播所有 R，参与者本地计算

    C->>S1: 广播 R_1, R_2, R_3
    C->>S2: 广播 R_1, R_2, R_3
    C->>S3: 广播 R_1, R_2, R_3

    S1->>S1: 计算 R_sum，判断奇偶取反 k_1
    S1->>S1: 计算 λ_1, e, z_1 = k_1 + e·λ_1·s_1
    S2->>S2: 计算 R_sum，判断奇偶取反 k_2
    S2->>S2: 计算 λ_2, e, z_2 = k_2 + e·λ_2·s_2
    S3->>S3: 计算 R_sum，判断奇偶取反 k_3
    S3->>S3: 计算 λ_3, e, z_3 = k_3 + e·λ_3·s_3

    S1-->>C: 返回 z_1
    S2-->>C: 返回 z_2
    S3-->>C: 返回 z_3

    C->>C: 聚合 z = z_1 + z_2 + z_3
    C->>C: 输出签名 sig = R_x, z
```

---

### 🔹 FROST 失败场景（无 Robustness）

```mermaid
sequenceDiagram
    participant C as 协调者
    participant S1 as Signer 1
    participant S2 as Signer 2
    participant S3 as Signer 3

    C->>S1: 广播 msg，请求 Nonce 承诺
    C->>S2: 广播 msg，请求 Nonce 承诺
    C->>S3: 广播 msg，请求 Nonce 承诺

    S1-->>C: 返回 R_1
    S2-->>C: 返回 R_2
    S3--xC: 不响应

    C->>C: 卡住，等待 S3...
    C->>C: 无 fallback、无重试机制
    C->>C: 会话失败
```

---

### 🔹 ROAST（具备 Robustness + Asynchronous）

```mermaid
sequenceDiagram
    participant R as ROAST Orchestrator
    participant A as Signer A
    participant B as Signer B
    participant C as Signer C
    participant D as Signer D

    Note over R: 尝试 1: 子集 A,B,C

    R->>A: 广播 msg，请求 Nonce 承诺
    R->>B: 广播 msg，请求 Nonce 承诺
    R->>C: 广播 msg，请求 Nonce 承诺

    A-->>R: 返回 R_A
    B-->>R: 返回 R_B
    C--xR: 不响应

    R->>R: 超时，尝试 1 失败，自动切换子集

    Note over R: 尝试 2: 子集 A,B,D

    R->>A: 广播 msg，请求 Nonce 承诺
    R->>B: 广播 msg，请求 Nonce 承诺
    R->>D: 广播 msg，请求 Nonce 承诺

    A-->>R: 返回 R_A
    B-->>R: 返回 R_B
    D-->>R: 返回 R_D

    R->>A: 广播 R_A, R_B, R_D
    R->>B: 广播 R_A, R_B, R_D
    R->>D: 广播 R_A, R_B, R_D

    A-->>R: 返回 z_A
    B-->>R: 返回 z_B
    D-->>R: 返回 z_D

    R->>R: 收到 t=3 份，聚合签名完成
```

---

## 📌 4. 本质区别（概念图）

```mermaid
mindmap
  root((FROST vs ROAST))
    FROST
      是：签名算法
      目的：紧凑的 t-of-n Schnorr 签名
      要求：参与者在线、同步
      问题：任何节点不响应则卡住
    ROAST
      是：FROST 的封装层
      目的：保证签名最终完成
      特性：自动重试和切换子集
      优势：支持异步网络和恶意节点
```

---

## 📌 5. 总结（一句话）

```mermaid
flowchart LR
    A[FROST] -->|提供签名算法| C[最终 Schnorr 门限签名]
    B[ROAST] -->|提供鲁棒性| C
    style A fill:#dfefff,stroke:#6b8fd6
    style B fill:#ffe8d6,stroke:#d67f35
    style C fill:#eaffea,stroke:#4f8f00
```

---
