# openai/langchain-ai-langchain

来自 [langchain-ai/langchain](https://github.com/langchain-ai/langchain)（MIT License）官方 `langchain-openai` 分包 `libs/partners/openai/tests/cassettes/`（原文件是 gzip 压缩的 `.yaml.gz`，这里存的是解压后的 `.yaml`）。共 99 个文件。

## 重要发现：大多数文件实际走的是 Responses API，不是 Chat Completions

按请求体逐个检查后发现：文件名前缀 `TestChatOpenAICodexStandard.*` 容易让人误以为是 Chat Completions（`ChatOpenAI` 是 LangChain 的类名）,但实际请求体绝大多数是 `{"input":[...{"type":"message"}],...}` 形状——**Responses API**,不是 `{"messages":[...]}` 的 Chat Completions。原因：这批测试用的是 `gpt-5.x`/codex 系列模型,LangChain 的 `ChatOpenAI` 包装类对这些模型会在内部自动路由到 `/v1/responses`,类名里的"Chat"指的是 LangChain 统一的对话模型接口,不代表调用的是 Chat Completions 端点。

**真正是 Chat Completions 形状（`"messages"` 字段）的只有 2 个**：`TestOpenAIStandard.test_stream_time.yaml`、`test_streaming_tool_call_v1_v2_parity.yaml`（都用 `gpt-4o-mini`，非 codex/reasoning 系列模型）。

对我们项目的含义：这批数据主要能核对的是 `internal/translator/identity/responses.go`（Responses↔Responses 原生透传 + `extractor.NewResponses()` 的 usage 提取）和 `internal/translator/responses_openai`（Responses 客户端 → Chat Completions 上游的转换器，注意它目前对 tools/非文本输入是故意 fail-fast，不是要"修 bug"，而是看真实客户端会发什么），而不是 `internal/protocol/openai` 原生 Chat Completions 适配器——那边能直接用的真实样本很少。

## 分类索引

### 真 Chat Completions（`"messages"` 形状）

| 文件 | 内容 |
|---|---|
| `TestOpenAIStandard.test_stream_time.yaml` | 基础流式响应，`stream_options.include_usage` |
| `test_streaming_tool_call_v1_v2_parity.yaml` | 流式 tool_choice 强制指定单个函数（SDK v1/v2 一致性测试） |

### Responses API —— 基础 invoke/stream

`TestChatOpenAICodexStandard.test_invoke*` / `test_ainvoke*` / `test_stream*` / `test_astream*` / `test_batch` / `test_abatch` / `test_conversation` / `test_double_messages_conversation` / `test_message_with_name` / `test_stop_sequence` / `test_bind_runnables_as_tools` / `test_stream_events_v3` / `test_astream_events_v3`（LangChain 自己的 Runnable/事件流接口测试，字段形状仍是 Responses 原生的）；`test_codex_invoke*` / `test_codex_stream*` / `test_codex_invoke_lifts_system_message_into_instructions`（system message 提升为 `instructions` 字段）/ `test_codex_invoke_with_instructions_override` / `test_codex_multi_turn_no_tools`（`previous_response_id` 续接）/ `test_codex_stream_events_v3*`

### Responses API —— 工具调用 / tool_choice

`TestChatOpenAICodexStandard.test_tool_calling*` / `test_tool_choice` / `test_tool_calling_with_no_arguments` / `test_tool_message_error_status` / `test_tool_message_histories_list_content` / `test_tool_message_histories_string_content` / `test_unicode_tool_call_integration`；`test_codex_function_calling` / `test_function_calling`

### Responses API —— 内置服务端工具

`test_mcp_builtin` / `test_mcp_builtin_zdr`（MCP，含 ZDR 模式）、`test_code_interpreter`（代码执行）、`test_web_search`（`{"type":"web_search_preview"}`）、`test_file_search`、`test_custom_tool` / `test_codex_custom_tool`、`test_apply_patch`（Codex 的 apply_patch 工具）、`test_tool_search` / `test_tool_search_streaming` / `test_client_executed_tool_search`

### Responses API —— reasoning

`test_reasoning`（注意：尽管没有 `codex` 前缀,实测也是 Responses 形状）、`test_codex_reasoning`、`test_codex_reasoning_summary_streaming`、`test_stream_reasoning_summary`、`test_reasoning_text_v1_v2_parity`、`test_phase` / `test_phase_streaming`（reasoning summary 分阶段输出）

### Responses API —— 结构化输出 / schema

`TestChatOpenAICodexStandard.test_structured_output[pydantic/json_schema/typeddict]`（+ `_async` 变体）、`test_structured_output_optional_param`、`test_structured_output_pydantic_2_v1`、`test_structured_few_shot_examples`、`test_codex_structured_output_pydantic` / `test_codex_structured_output_typed_dict`、`test_parsed_pydantic_schema`、`test_schema_parsing_failures_responses_api` / `_async`

### Responses API —— 多轮状态 / 压缩 / 异常状态

`test_compaction` / `test_compaction_streaming`（上下文压缩）、`test_incomplete_response`（`status:"incomplete"`）、`test_agent_loop` / `test_agent_loop_streaming` / `test_codex_agent_loop` / `test_codex_agent_loop_streaming`（多轮 agent 循环）

### Responses API —— 图像生成

`test_image_generation_multi_turn` / `test_image_generation_streaming`

### 多模态输入（未逐个确认 Chat 还是 Responses 形状，两者都可能）

`TestChatOpenAICodexStandard.test_anthropic_inputs`（LangChain 的跨供应商输入格式适配）、`test_audio_inputs`、`test_image_inputs` / `test_image_tool_message`、`test_pdf_inputs` / `test_pdf_tool_message`

### 非 chat/responses（Embeddings API + Chat Completions 侧的 schema 失败测试）

`test_langchain_openai_embeddings_equivalent_to_raw` / `_async`（Embeddings API，不是 chat/responses）、`test_schema_parsing_failures` / `_async`（Chat Completions 侧的结构化输出解析失败，和上面 `_responses_api` 变体对应）

## 怎么用

同上级目录 README：大部分文件是 `requests`/`responses` 两个平行数组格式（新格式），body 多数是纯文本 bytes（未 gzip），个别大响应体可能是 gzip。用 `yaml.safe_load` 读，鉴权头已被 pytest-recording 替换成 `**REDACTED**`（已核对全部文件，无真实密钥泄露）。
