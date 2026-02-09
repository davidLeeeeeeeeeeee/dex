---
description: 构建并运行节点、Explorer 或模拟器
---

# 构建并运行

## 1. 编译主节点

```bash
go build -o dex.exe ./cmd/main
```

## 2. 运行主节点（开发模式）

```bash
./dex.exe --data-dir ./data --port 7000
```

## 3. 编译并运行 Explorer

```bash
# 后端
go build -o explorer.exe ./cmd/explorer
./explorer.exe --node-data-dir ./data --port 8080

# 前端开发服务器
cd explorer
npm run dev
```

## 4. 编译并运行模拟器

```bash
go build -o simulateMain.exe ./simulateMain
./simulateMain.exe
```

## 5. 重新生成 Protobuf

需要先安装 `protoc` 和 `protoc-gen-go` 插件。

```bash
protoc --go_out=. --go_opt=paths=source_relative pb/data.proto
```

## 6. 运行测试

```bash
# 全部测试
go test ./... -count=1

# 指定模块测试
go test ./matching/... -v
go test ./vm/... -v -run TestXxx
go test ./consensus/... -v
```

## 7. 清理数据目录（重置状态）

⚠️ 这会删除所有本地链数据，仅在开发环境使用：

```bash
Remove-Item -Recurse -Force ./data
Remove-Item -Recurse -Force ./explorer_data
```
