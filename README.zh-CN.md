# opencassette

真实录制的 LLM API 流量——开放语料库 + 工具链：**录制**、**脱敏**、**验真**、**加载**。

## 为什么做这个

所有在 LLM API 之间做转换的东西(网关、SDK、代理、Agent 框架)都需要真实的请求/响应数据来测试——厂商文档和手写 fixture 都会和线上真实字节漂移。但这类数据today几乎不存在共享形态：

- langchain 系、simonw 的 `llm-*` 插件的 cassette 只是测试套件的副产品——分散、格式不一、只覆盖它们恰好集成的厂商;
- litellm 录真实流量,但录进带 24h TTL 的 Redis,刻意不留档;
- DeepSeek / 智谱 GLM / MiniMax 等整个生态**一份公开录制都没有**——需求真实到已经有公开仓库在提交手写的假"录制"(`chatcmpl-verify-001` 这种占位 id、epoch 占位时间戳),假数据比没有数据更糟。

opencassette 补上这一块：带溯源的标准落盘格式、内置双重脱敏的录制器、让每次录制覆盖真实 SDK 参数面的场景包、针对野外实际观察到的造假手法调校的验真器,以及一个可持续生长的语料库布局。

## 内容

- **`cassette`(公开包)**：加载野外两种落盘格式(pytest-recording 的 `interactions:`、langchain 的平行列表),body 归一化(嵌套/`!!binary`/gzip)——对外唯一可 import、用于回放的包
- **`internal/recorder`**：`http.RoundTripper` 形态的录制器,脱敏全部委托给 `redact`,写入 `meta:` 溯源块
- **`internal/redact` + `profiles/`**：脱敏策略——内置跨厂商 baseline(凭证头,以及 trace/关联载体一致改写为 `**TRACE_ID_n**`)、按厂商叠加的 `profiles/<vendor>.yaml`、以及自定义 header/find/pattern 替换,均只作用于录制副本
- **`internal/scenario` + `packs/`**：SDK 派生、测试强制覆盖度的标准请求场景包(工具调用/工具回环/流式/结构化输出,不只是 "hi")
- **`internal/verify`**：检查凭证泄漏与合成数据特征(占位 id、不可能的时间戳、对不上的 token 数)
- **`cmd/opencassette`**：CLI(`record` / `verify`)
- **`corpus/`**：录制数据本体,布局 `vendor/model/protocol/{stream,nostream}/场景.yaml`

## 快速开始

```sh
go build -o opencassette ./cmd/opencassette

# 对一个厂商批量录制整个场景包
RECORD_API_KEY=sk-... ./opencassette record \
  --url https://api.deepseek.com/chat/completions \
  --scenario-dir packs/openai-chat \
  --vendor deepseek --model deepseek-chat

# 验真(CI 对每个 PR 跑这个)
./opencassette verify corpus
```

### 脱敏与厂商 profile

每次录制都用内置跨厂商 baseline 脱敏:凭证头置为 `**REDACTED**`,trace/关联载体(`traceparent`、B3、`X-Request-Id` 等)一致改写为 `**TRACE_ID_n**`(跨 header、URI、body)。baseline(`internal/redact/baseline.yaml`)编译进二进制,无 profile 也兜底。厂商可在 `profiles/<vendor>.yaml` 声明额外载体,`--vendor` 自动加载:

```yaml
# profiles/azure.yaml
trace_headers:
  - apim-request-id
  - azureml-model-session
replacements:                    # 自定义替换,只作用于录制副本
  - header: x-ms-region          # 按 header 名整值替换
    with: "**REGION**"
  - pattern: 'org-[a-z0-9]{24}'  # 正则,仅 body
    with: "**ORG_ID**"
    in: [body]
```

替换在 baseline 脱敏之后执行,只改录制副本,绝不动真实请求/响应,也不动 `meta` 溯源块。

## 贡献录制数据

见 [CONTRIBUTING.md](CONTRIBUTING.md)。一句话：必须是真的(验真器和评审流程都为此存在)、必须脱敏、必须带溯源;**不收合成数据**——那会摧毁这个项目存在的意义。

## License

Apache-2.0。录制内容含模型生成输出;任何录制不得包含凭证或个人数据。
