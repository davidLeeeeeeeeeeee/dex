.
├── cmd/
│   ├── node/              # 全节点二进制：引导、配置、依赖注入、运行主循环
│   └── tool/              # 小工具：创世生成、密钥、离线签名、导入导出等
├── internal/              # 仅对本仓库可见的业务实现（可自由重构）
│   ├── node/              # 组合各子系统的“应用层”：生命周期、DI、服务编排
│   ├── consensus/
│   │   ├── snowman/       # 你的共识引擎实现（Snowball/Snowman）
│   │   └── iface/         # 共识接口（Engine/BlockView 等）
│   ├── p2p/               # 节点间网络：传输层、消息编解码、对等体管理
│   ├── storage/
│   │   ├── block/         # 区块/状态存储（内存实现 + 未来可插拔 DB）
│   │   └── db/            # 账户/交易/索引等（Badger 实现与管理器）
│   ├── mempool/           # 交易池（去重、优先级、打包挑选）
│   ├── syncer/            # 链同步（落后识别、批量回填、限流管线）
│   ├── gossip/            # 区块/交易的传播策略
│   ├── rpc/
│   │   ├── http/          # HTTP/Proto 处理器（GetBlock/GetData等）
│   │   └── auth/          # 认证/白名单/速率限制
│   ├── events/            # 事件总线（发布订阅）
│   ├── telemetry/         # 指标、日志、Tracing（Prom/pprof/zap）
│   ├── crypto/            # 密钥/签名/证书（自签发工具等）
│   ├── config/            # 配置模型与加载（env/flags/file 混合）
│   └── types/             # 公共基础类型（Block、Message、IDs、错误等）
├── pkg/                   # “稳定 API” 工具库（给外部或未来子项目复用）
│   ├── params/            # 链参数（网络常量、阈值、费用规则）
│   └── utils/             # 通用小工具（序列化、编码、校验）
├── proto/                 # .proto 源文件（生成 *.pb.go 到 internal 或 pkg）
├── scripts/               # 开发脚本：一键编译、生成证书、启动本地多节点
├── docs/                  # 设计文档、运行指南、接口说明
└── Makefile               # 编译、测试、lint、protoc、镜像等任务
