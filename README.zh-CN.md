# Codex Space Bunny Proxy

[English](README.md)

基于 [azain47/zen-proxy v0.3.0](https://github.com/azain47/zen-proxy) 的非官方兼容分支，主要修复 Codex CLI 调用 OpenCode Zen `space-bunny-free` 时的请求兼容问题。

## 它做什么

补齐兼容的客户端元数据，并把 `bash`、`read` 转换为 **Codex 真正执行的工具调用**。`bash` 对应 `exec_command`，`read` 对应参数安全编码后的 Python 文件读取命令。代理自身不执行命令、不读取项目文件；执行、沙箱和审批仍由 Codex 处理。

## 安装与启动

需要 Go 1.22+、Codex CLI 和 Python 3。当前实测环境为 Linux、Codex CLI 0.162.1。

```sh
git clone https://github.com/jackwangfeng/codex-space-bunny-proxy.git
cd codex-space-bunny-proxy
go build -o zen-proxy ./cmd/zen-proxy
ZEN_SPACE_BUNNY_COMPAT=1 ZEN_MODEL=space-bunny-free ./zen-proxy
```

代理默认监听本机 `127.0.0.1:8788`。另开终端，在仓库目录运行：

```sh
sh scripts/install-codex-profile.sh
codex --profile space-bunny
```

脚本生成独立的 `space-bunny.config.toml`，不替换默认 `config.toml`。已有同名配置请先备份。端口被占用时，通过 `ZEN_PORT` 和 `ZEN_PROXY_CODEX_BASE_URL` 设置匹配的地址。

如需出网代理，额外设置 `ZEN_SPACE_BUNNY_HTTP_PROXY=http://127.0.0.1:你的代理端口`。它只影响兼容的 Space 请求，不改变其他模型的出网方式。

## 验证与边界

```sh
go test -race ./...
go vet ./...
```

两台 Linux 主机已实测：Codex 启动、多轮文件读取、修改代码、运行测试。工具名、参数、历史消息和强制工具选择都有对应转换测试。文字和思考内容流式转发，工具参数在完整收到后进行转换。

只有启用 `ZEN_SPACE_BUNNY_COMPAT=1`、模型为 `space-bunny-free`，且客户端声明真实的 `exec_command` 时才启用新功能。不伪造无法执行的空工具。普通无工具 API 请求仍可能被服务端拒绝。

这是非官方兼容层，不是 OpenCode 本体；上游规则变化可能再次影响兼容性。额度限流、模型退役、账号权限和服务故障仍按上游实际结果处理。不是无限额度工具。仅验证本项目新增的 Space 兼容功能，不宣称所有免费模型均可用。

## 开源来源

原项目作者 azain47 及贡献者，MIT 许可证；Space 兼容改动由 jackwangfeng 提供。保留原作者署名和许可证。内嵌的 Codex 提示词另保留 Apache-2.0 声明，见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
