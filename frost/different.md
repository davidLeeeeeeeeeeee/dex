
原文https://www.btcstudy.org/2022/11/04/robust-asynchronous-schnorr-threshold-signature-tabconf-2022/
---

# Frost vs Roast — 本质区别说明（Mermaid 可视化）

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
        note right of F1
            单轮失败 → 会话卡住  
            无 retry 机制  
            无 subset 切换  
        end note
    end

    subgraph ROAST
        R0[ROAST Orchestrator]
        R1[Signer A]
        R2[Signer B]
        R3[Signer C]
        R4[Signer D]
        R5[Signer E]

        R0 -->|尝试 1<br>(子集: A,B,C)| R1
        R0 --> R2
        R0 --> R3

        R0 -->|尝试 2<br>(子集: B,D,E)| R2
        R0 --> R4
        R0 --> R5

        R1 --> R0
        R2 --> R0
        R3 --> R0
        R4 --> R0
        R5 --> R0

        note right of R0
            可能进行多次 FROST 会话  
            自动跳过不响应节点  
            至少 t 个 honest 节点 → 成功  
        end note
    end
```

---

## 📌 3. 签名流程对比（时序图）

### 🔹 FROST（不具备 Robustness）

```mermaid
sequenceDiagram
    participant C as 协调者
    participant S1 as Signer 1
    participant S2 as Signer 2
    participant S3 as Signer 3

    C->>S1: 请求 Nonce Commit (Round 1)
    C->>S2: 请求 Nonce Commit
    C->>S3: 请求 Nonce Commit

    S1-->>C: 返回 Commit
    S2-->>C: 返回 Commit
    S3-->>C: （不响应）

    C->>C: 卡住，等待 S3…
    C->>C: 无 fallback、无重试机制 → 会话失败
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

    Note over R: 尝试 1 (子集: A,B,C)

    R->>A: FROST 会话 #1
    R->>B: FROST 会话 #1
    R->>C: FROST 会话 #1

    A-->>R: 返回签名材料
    B-->>R: 返回签名材料
    C-->>R: （不响应）

    R->>R: 尝试 1 失败 → 自动切换子集

    Note over R: 尝试 2 (子集: A,B,D)

    R->>A: FROST 会话 #2
    R->>B: FROST 会话 #2
    R->>D: FROST 会话 #2

    A-->>R: 返回签名
    B-->>R: 返回签名
    D-->>R: 返回签名

    R->>R: 收到 t=3 份 → 签名完成
```

---

## 📌 4. 本质区别（概念图）

```mermaid
mindmap
  root((FROST vs ROAST))
    FROST
      "是：签名算法"
      "目的：紧凑的 t-of-n Schnorr 签名"
      "要求：参与者在线、同步"
      "问题：任何节点不响应 → 卡住"
    ROAST
      "是：FROST 的封装层"
      "目的：保证签名最终完成 (liveness)"
      "特性：自动重试/切换子集"
      "优势：支持异步网络、恶意节点"
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
