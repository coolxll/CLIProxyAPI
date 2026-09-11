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

### 第一阶段：CPA 内部解耦与去重（低风险优先）

1. **创建公共协议叶子包 `sdk/lingmawire/`**：
   ```text
   sdk/lingmawire/
   ├── envelope.go    # SSE data: 前缀处理、body 双层解包、[DONE] 检查
   ├── error.go       # ParseLingmaError、ResolveLingmaErrorDetails、LingmaErrorInfo
   ├── messages.go    # NormalizeAssistantToolCallContent、AgentID
   ├── usage.go       # SSE 流式/非流式 usage 提取与归一化
   └── testdata/      # 协议级共享 Contract Fixtures
   ```

2. **建立协议级 Contract Fixtures 与共享断言测试**：
   - `provider_error_details_string.sse`（转义 JSON 字符串 details）
   - `provider_error_details_object.sse`（嵌套对象 details）
   - `assistant_null_tool_content.json`（tool_calls content 归一化输入样本）
   - `tool_call_stream.sse`（流式 tool call 帧）
   - `usage_variants.sse`（多版本 usage 输出）

3. **改造原生 translator (`internal/translator/lingma/`)**：
   - 依赖 `sdk/lingmawire`
   - 同步修复原生 translator 中遗留的 details 展开与 tool call null content 问题

4. **改造插件 codec (`provider-plugins/internal/lingma/`)**：
   - 替换 `internal/lingma/codec/helpers/` 及相关模块，全面接入 `sdk/lingmawire`
   - `executor.go` 的错误识别直接复用 `lingmawire.ParseLingmaError`

5. **验证与回归**：
   - 确保 `native_parity_test.go`、`request_parity_test.go`、`response_parity_test.go` 保持通过
   - `go test ./...` 与 `cmd/server`、`cmd/lingma` 编译校验

---

### 第二阶段：抽取独立 Module 供 LingmaTap 共享

- 将稳定后的 `sdk/lingmawire` 提取为独立小型 Go module（如 `lingma-protocol-go`）
- `CLIProxyAPI` 与 `LingmaTap` 均通过 Go 模块版本依赖引入，通过版本升级保持多端修复与协议演进同步
