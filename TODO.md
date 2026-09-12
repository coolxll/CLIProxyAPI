# TODO: Lingma Wire Protocol 抽离与多端去重重构

## 背景与问题

当前 Lingma 在各端实现存在极高代码重复：
- **OpenAI response codec**：原生版（`internal/translator/lingma/openai/responses`）与插件版（`provider-plugins/internal/lingma/codec/openai/response`）均为 ~737 行，实际仅有少量差异。
- **OpenAI request codec**：原生版与插件版结构与逻辑近乎镜像。
- **Claude request/response**：两端同样高度重叠。
- **双/三端维护成本高**：当前依靠 parity tests 验证一致性，但每次 Bug 修复或协议适配都需要在原生版、插件版以及外部项目（如 LingmaTap）各改一次。例如本次解决的“上游 `details` 错误解析与展开”和“assistant tool-call `content:null → ""` 归一化”，目前已在 plugin 端修复并验证，但原生 translator 仍存在相同缺陷。

---

## 目标架构：共享协议核心 + 薄适配器

```text
lingmawire (共享协议核心)
├── CLIProxyAPI native adapter (internal/translator/lingma)
├── CLIProxyAPI plugin adapter (provider-plugins/internal/lingma)
└── LingmaTap adapter (独立 Go module / 外部消费)
```

### 职责边界

- **`lingmawire` 共享协议核心（纯 Wire Protocol，无副作用，无外部重量级依赖）**：
  - SSE envelope / body 双层 JSON 解包
  - `details` 错误结构化展开与状态码判定（JSON 字符串、嵌套对象、纯文本容错）
  - Assistant tool-call `content: null → ""` 规范化（安全只读克隆，严格限定 assistant + 非空 tool_calls + 空 content）
  - `[DONE]` 流结束标记判断
  - Usage / tokens 统计解析与归一化
  - Lingma 请求公共参数、Agent ID 推断与基础模型配置
  - **不包含**：HTTP handler、日志框架、重试策略、数据库/存储以及下游 API 输出组装

- **各适配器保留职责**：
  - OpenAI / Claude / Codex / Responses 下游响应封装与输出
  - Gateway Log / 审计日志
  - 重试、降级与 Thinking Fallback 策略
  - Auth / Token / Session 管理
  - Plugin Host RPC 通信

---

## 落地规划

### 第一阶段：CPA 内部解耦与去重（低风险优先）- [x] 已完成

1. **创建公共协议叶子包 `sdk/lingmawire/`**：[x]
   ```text
   sdk/lingmawire/
   ├── envelope.go    # SSE data: 前缀处理、body 双层解包、[DONE] 检查
   ├── error.go       # ParseError、ResolveErrorDetails、ErrorInfo
   ├── messages.go    # NormalizeAssistantToolCallContent、AgentID
   ├── usage.go       # SSE 流式/非流式 usage 提取与归一化
   └── testdata/      # 协议级共享 Contract Fixtures
   ```

2. **建立协议级 Contract Fixtures 与共享断言测试**：[x]
   - `provider_error_details_string.sse`（转义 JSON 字符串 details）
   - `provider_error_details_object.sse`（嵌套对象 details）
   - `assistant_null_tool_content.json`（tool_calls content 归一化输入样本）
   - `tool_call_stream.sse`（流式 tool call 帧）
   - `usage_variants.sse`（多版本 usage 输出）
   - `sdk/lingmawire/lingmawire_test.go` 全绿通过

3. **改造原生 translator (`internal/translator/lingma/`)**：[x]
   - 依赖 `sdk/lingmawire`
   - 同步修复原生 translator 中遗留的 details 展开与 tool call null content 问题

4. **改造插件 codec (`provider-plugins/internal/lingma/`)**：[x]
   - `internal/lingma/codec/helpers/` 及相关模块全面接入 `sdk/lingmawire`
   - `executor.go` 的错误识别直接复用 `lingmawire.ParseError`

5. **验证与回归**：[x]
   - `native_parity_test.go`、`request_parity_test.go`、`response_parity_test.go` 全绿通过
   - `go test ./...` 与 `cmd/server`、`cmd/lingma` 编译校验全绿通过

---

#### 第二阶段：抽取独立 Module 供 LingmaTap 共享 - [x] 已完成
 
 - [x] 将稳定后的 `sdk/lingmawire` 提取为独立小型 Go module（`github.com/coolxll/lingma-protocol-go`）
 - [x] `CLIProxyAPI` 与 `provider-plugins` 均通过 Go 模块版本依赖引入，完成多端去重重构
 
 ---

# TODO: Provider 插件模型列表诚实化（无兜底目录 / 无静默换模型）

## 原则

1. 插件不得凭空造/缓存模型列表（不写静态兜底目录）；模型列表只能来自 live 上游。
2. 上游失败就报错（宿主记 warning、不注册模型、不弹 auth），不得静默回退。
3. 不得静默替换模型：假别名映射、自动 failover 换模型都要消灭或改为显式。

## 待办

### 1. 删除三个插件的兜底模型目录 - [x] 已完成并提交 commit 9d431f3e（待部署）

- `trae`：`models.go` live 失败直接返回 err；删 `static_models.go`（假 `gpt-4o` / `claude-3-5-sonnet` / `auto` 别名源头）；`plugin.go` 无兜底；`model.static` RPC 返回空。
- `opencode`：删 `staticModels()`，`fetchCloudZenModels` / `fetchDaemonModels` 5 处兜底改报错；保留 `verifiedAvailableFreeModels` 过滤与 `opencode/free` live 路径。
- `openrouter`：删 `fallbackFreeModels()`（6 个硬编码 `:free` + 虚拟路由兜底）。
- `lingma`：本来就无兜底，未动。
- 测试：删 `TestStaticModels`，重写 openrouter 相关测试；`go test -count=1` trae/opencode/openrouter 全绿。
- 宿主侧行为已验证：`sdk/cliproxy/service_executors.go:496` 出错时不注册模型且不禁用 auth；`internal/pluginhost/adapters.go:261` 打日志。

### 2. A：OpenRouter 虚拟路由候选池改 live - [x] 已完成并提交 commit 9d431f3e（待部署）

目标：`openrouter/free`、`openrouter/free:coding` / `:reasoning` / `:fast` 等虚拟路由只从**本 auth 的 live 免费模型**中选；live 无免费模型则不暴露虚拟路由，请求时报明确错误；不再静默换模型。

已完成：
- [x] 新增 `internal/openrouter/live_models.go`：按 `stableAuthID` 缓存 live 免费模型（TTL 5min）；`resolveVirtualModel` 改为 live ∩ 排行榜（catch-all 额外接受无排行榜条目的 live 免费模型）；`resolveRequestedModel` / `nextLiveFallback`。
- [x] `models.go`：`fetchModels` 成功后 `storeLiveModels`；`parseOpenRouterModels` 先解析上游，**仅有免费模型时**才前置虚拟路由条目。
- [x] `cooldown.go`：`nextFallbackModel` 删掉目录兜底与最后兜底返回字面量 `openrouter/free`（发上游必然报错），无 live 候选则返回 `""` → 调用方报错；严格过滤候选集中的虚拟路由别名。
- [x] `executor.go`：4 处（execute / execute_stream 的解析点与 429 重试点）改用 live 感知解析。
- [x] `quality.go`：删除旧版重复的 `resolveVirtualModel`（与 live_models.go 重名）与 `qualityPreservingFallback`（纯目录兜底，已无调用方）。
- [x] 更新并补全测试：
  - `cooldown_test.go`：验证 live fallback 优先就近 tier；live 候选耗尽后返回 `""` 报错，不再期望 `openrouter/free` 兜底。
  - `quality_test.go`：改为传 live 集，补充 coding 候选，删除废弃函数测试。
  - `executor_test.go`：mock `/models` 返回 live 列表（defaults to `testLiveModelsJSON`）。
  - `live_models_test.go`：覆盖 `normalizeModelID`、`storeLiveModels` 过滤付费/虚拟路由、live ∩ 排行榜解析、无 live 免费模型时报错拒选。
- [x] `go test -count=1 ./...` 在 `provider-plugins` 目录下全部包（trae / opencode / openrouter / lingma）全绿通过。

剩余待办：
- [x] 提交 commit（包含模型诚实化、OpenRouter live 路由、Trae 假别名清理与 DeviceID 对齐改造全部变动，commit `9d431f3e`）。

### 3. D：`auto_failover` 配置止血（openrouter）- [ ] 阻塞

- 在 openrouter auth JSON 里加 `"auto_failover": false`（默认 true，会静默换模型重试）。auth 文件模板：
  ```json
  {"type":"openrouter-plugin","name":"openrouter","api_key":"sk-or-v1-...","free_only":true,"auto_failover":false}
  ```
- 阻塞原因：corp172-dev 上**没有 openrouter auth 文件**，无处可配；等建 auth 时一并带上。

### 4. trae 假协议别名清理 - [x] 已完成并提交 commit 9d431f3e（待部署）

清理内容：
- [x] 删除 `protocol.go` 中的假别名映射：`auto` / `claude-3-5-sonnet` / `gpt-4o` → `glm-5.2`（不再凭空伪造/冒充模型）。
- [x] 删除 `protocol.go` 中的 V2 未知模型兜底：删掉 `protocol == traeProtocolV2` 下强制路由至 `no_thinking_model`（`title_generation`）的 catch-all，未知模型诚实原样透传。
- [x] 删除 `protocol.go` 中的 family 模糊短别名集合（`Explicit Aliases`）：删掉 `glm`、`kimi`、`qwen`、`doubao`、`deepseek`、`minimax` 及其衍生短写，不再自动猜模型。
- [x] 删除 `resolveRawChatModelConfig` V1 中的模糊短写：`r1`、`reasoner`、`deepseek`、`v3`、`v3-0324`。
- [x] 更新并补充测试：`protocol_test.go` 新增对 `gpt-4o`、`claude-3-5-sonnet`、`glm`、`kimi`、`qwen`、`deepseek` 诚实透传的断言。
- [x] `go test -count=1 ./internal/trae/...` 验证通过。

### 5. （可选）openrouter executor 免费模型门控 - [ ] 待决定

`free_only` 目前只影响模型列表，executor 不做限制：传付费模型 ID 会真的花钱。可镜像 opencode 的 `verifiedAvailableFreeModels` 做法。

### 6. 构建与部署到 corp172-dev - [ ] 等代码定稿

- 在 corp172-dev 的 golang 容器里构建（本机无 Docker；`go.mod` 有 `replace ... => ..`，需整个仓库）：`CGO_ENABLED=1 go build -buildmode=c-shared -o bin/<name>-plugin-v0.2.0.so ./cmd/<name>`。
- 拷 4 个 `.so` 到 `~/workspace/homelab-secrets/cpa/plugins/linux/amd64/`，然后 `docker compose up -d`（不是 restart）。
- [ ] 部署后验收：4 个插件都 loaded；`/v1/models` 与 `GET /v0/management/auth-files/models?name=<auth>.json` 对得上；trae 凭据失效应表现为“模型列表为空 + warning”而不是出现 `gpt-4o` 等假模型；openrouter 无 auth 则不应出现任何 openrouter 条目。

### 7. trae 凭据失效原因与重新登录 - [ ] 阻塞于人工操作

- **失效原因调查结论**：
  - corp172-dev 上的凭据于今日 `13:37:23` 刚创建（JWT `exp: 2026-09-26`，仍有 14 天有效期）。
  - 但随后在首次请求时即被上游拒绝（401 / `refresh token is invalid`，StandardCode `040012`）。
  - 核心原因：Trae 服务端有强设备绑定风控（OAuth 回调中返回的 `BoundDeviceID: qwxe24l3old7ow` 与服务端绑定的 session 对应；而插件本地生成并发送了不同的数字型 `device_id` `3924235236021673`；且 Trae 账号通常具备单设备排他/踢下线机制，如果在其他客户端或浏览器中有重新登录/退出行为，旧 refresh token 会被立刻注销）。
- **重新登录方式**：
  浏览器打开：`GET /v0/management/trae-plugin-auth-url` → 访问返回的 Trae OAuth 授权链接并完成授权 → 自动回调 `/v0/management/oauth-callback` 更新凭据文件。

### 8. trae 插件 DeviceID 对齐 - [x] 已完成并提交 commit 9d431f3e（待部署）

- **对齐改造内容**：
  - [x] `login.go`：新增 `extractCallbackDeviceInfo` 函数，在 `pollLogin` 接收回调时支持解析 `userJwt` JSON 以及直接 query 参数中的 `BoundDeviceID`、`deviceId` 等。若授权端下发了服务端认定的 `BoundDeviceID`（如 `qwxe24l3old7ow`），优先存入 `creds.DeviceID`，避免被假数字 ID 替换。
  - [x] `credentials.go`：在 `refreshToken` 解析 `ExchangeToken` 响应时提取 `BoundDeviceID`；严格保护已绑定的真实 `DeviceID`，只有在 `creds.DeviceID` 为空或等于旧硬编码 `defaultTraeID`（`2569994131757818`）时才降级使用 `deriveStableDeviceID` 生成哈希 ID；同时优化 `claimCheckinCredits` 的 fallback 判定。
  - [x] `plugin.go`：`parseAuth` 与 `refreshAuth` 完整保留真实绑定的 `DeviceID`；并在 `sanitizedMetadata` 中包含实际绑定的 `device_id`，方便管理面板和调试核对。
  - [x] `oauth_callback.go`：回调参数检查中补充 `userJwt` 支持，确保包含 `userJwt` 的回调参数完整传给 `.oauth` 文件。
  - [x] 单元测试与验证：
    - `login_test.go` 新增 `TestExtractCallbackDeviceInfo` 和 `TestTraeLoginPollWithBoundDeviceID`（端到端验证从回调 `userJwt` 提取 `BoundDeviceID` 并传递给 `checkin_credits` 和凭据存储）。
    - `credentials_test.go` 新增 `TestRefreshTokenWithUpstreamBoundDeviceID`、`TestRefreshTokenPreservesExistingBoundDeviceID`、`TestRefreshTokenFallbackDerivesStableDeviceID`。
    - `go test -count=1 ./internal/trae/...` 全绿通过。

## 关键事实（便于下次接手）

- 仓库：`CLIProxyAPI/provider-plugins`，包 `internal/{trae,opencode,openrouter,lingma}`；`main.go` 在 `cmd/<name>/`。
- 宿主无任何硬编码插件模型 ID（已 grep 验证）。
- 模型列表查询：`GET /v0/management/auth-files/models?name=<file>.json`；隐藏模型：`oauth-excluded-models: {trae-plugin: [...]}`。
- trae live 凭据在 corp172-dev 上已失效（refresh token invalid，StandardCode 040012），不是代码问题。
- openrouter live：约 448 个模型、25 个免费；`knownModelRankings` 为裸 ID（如 `deepseek/deepseek-r1:free`），live/列表 ID 带 `openrouter/` 前缀 → 匹配前用 `normalizeModelID` 去前缀 + 小写。
