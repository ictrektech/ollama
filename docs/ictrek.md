# ictrek Ollama

此 fork 将 `ollama_server` 的 gateway 能力内嵌到 Go 服务。容器内 `11434` 和 `11535` 共用同一服务、模型调度器和路由，两个端口的功能相同。原生 API、OpenAI API、JSON `/metrics`、`X-Task-Id`、请求级原生 `options`、客户端断连取消和 Model Hub 注册均由 Go 实现。

独立二进制默认仍只监听 `OLLAMA_HOST`；设置 `OLLAMA_COMPAT_LISTEN=0.0.0.0:11535` 开启第二个端口。镜像默认开启两个端口。

## Rerank API

`POST /v1/rerank`、`POST /rerank`、`POST /api/rerank` 是同一接口，使用 vLLM/Cohere/Jina 风格的请求与结果格式：

```sh
ollama pull qllama/bge-reranker-v2-m3:q8_0
curl http://localhost:11434/v1/rerank -H 'Content-Type: application/json' -d '{
  "model":"qllama/bge-reranker-v2-m3:q8_0",
  "query":"What is the capital of France?",
  "documents":["Bananas are yellow.","Paris is the capital of France."],
  "top_n":1,
  "return_documents":true
}'
```

返回 `{"model":"...","object":"list","results":[{"index":1,"relevance_score":0.9,"document":{"text":"Paris is the capital of France."}}],"usage":{"prompt_tokens":123,"total_tokens":123}}`，数值仅为格式示例。`index` 始终对应原输入顺序；结果按分数降序排列。`documents` 可传字符串数组或 `{"text":"..."}` 对象数组；`top_n` 不传则返回全部，传入时必须为正数。`return_documents` 默认 false。

支持 rank-pooling GGUF 交叉编码器及 Qwen3-Reranker；旧 BERT GGUF 的单输出分类头也会被识别。Qwen3 示例：`ollama pull AuditAid/Qwen3_Reranker:0.6B_Q8`。BGE 的分类头 logits 经 sigmoid 转为 0..1 分数，Qwen3 使用训练时 prompt 的 yes/no softmax 概率。普通 embedding 模型不作为 reranker。长输入超过后端上限会返回错误，不悄悄截断或用 embedding 第一维代替分数。

可选 Ollama 扩展：`options`、`keep_alive`；Qwen3 可用 `instruction` 改变检索任务说明。`options.num_ctx`、`num_gpu` 等按原生请求处理。

WeKnora 可选择通用 OpenAI 兼容 provider，Base URL 填 `http://<service>:11434/v1` 或 `11535/v1`，rerank score scale 选 `probability`。无需 Ollama embedding 适配器。WeKnora 已有的 `provider: ollama` 和 local rerank 行会转为同一通用协议，旧 `ollama_rerank_template` 被忽略。

## Gateway 兼容

- `/v1/chat/completions` 和 `/v1/completions` 接受原生 `options`，其优先级高于映射的 OpenAI 采样参数；支持 `keep_alive`、`think`、`format`。chat 支持 `max_completion_tokens`。
- 保留客户端 `X-Task-Id`，缺省自动生成；响应头返回同一 ID，并在 CORS 中允许、暴露该头。
- `/metrics` 保留旧 gateway 的 JSON 字段：`active_slots`、`total_slots`、`available_slots`、`slot_usage`、`active_by_model`、`phase`、`phase_by_model`、`model_metrics`、`requests`。统计覆盖本地原生 Chat/Generate、其 OpenAI 路径及 rerank。生成中 token/s 为估值（`estimated:true`），最终使用原生 token/duration；请求完成、出错或断连会移除活跃记录。排队及加载阶段仍显示为 prefill，槽位总数来自 `OLLAMA_NUM_PARALLEL`，默认 1。
- `MODEL_HUB_REGISTER_URL` 为空时不注册；沿用 `MODEL_HUB_SERVICE_ID/NAME/BASE_URL/GATEWAY_URL/ROLE`、`MODEL_HUB_REGISTER_ATTEMPTS`、`MODEL_HUB_REGISTER_RETRY_INTERVAL` 和原 metadata 环境变量。默认 base/gateway URL 均为 `http://<hostname>:11434`；两个端口等价，可显式设置 gateway URL 为 `11535`。

## 统一 CI 与版本

```sh
bash scripts/ictrek_release.sh check
# 在干净且已推送的分支上运行全构建、真实模型验证及镜像构建，不发布：
bash scripts/ictrek_release.sh build v0.40.3_20261012
# 创建并推送版本 tag，触发同样验证，通过后发布 SWR、飞书及 GitHub Release：
bash scripts/ictrek_release.sh release v0.40.3_20261012
bash scripts/ictrek_release.sh status
```

自有流程 `.github/workflows/ictrek-release.yaml` 使用 GitHub-hosted amd64/arm64 runner；不依赖上游专用 runner、Docker Hub、macOS/Windows 签名。复用官方 Dockerfile 的 CPU、CUDA 12/13、JetPack 5/6 构建目标和 CMake presets，CPU/CUDA 的 amd64 基础开发镜像换为 AlmaLinux 8，避免加载无关 ROCm 工具链。

产物：amd64/arm64 的 CPU+CUDA 主包，以及 JetPack 5/6 附加包、`sha256sum.txt`。版本写入二进制、OCI label、Release tag/title，格式为 `vX.Y.Z_YYYYMMDD`。正式 Release 保留完整 tag，不使用官方候选版本归并逻辑。自有 tag 不触发官方完整跨平台 release 或官方 Docker latest 流程。

## 镜像与飞书

`scripts/build_image.sh` 已迁入此 fork，从经校验的 fork Release 包组装纯 Go 镜像，不包含 Python gateway，不下载官方二进制。脚本直接在构建机器执行；同步由调用者负责，脚本不运行 SSH、rsync 或 Git。

```sh
bash scripts/build_image.sh --target amd --version v0.40.3_20261012 --package-dir /path/to/release-assets
bash scripts/build_image.sh --target arm --version v0.40.3_20261012 --package-dir /path/to/release-assets
bash scripts/build_image.sh --target l4t --version v0.40.3_20261012 --package-dir /path/to/release-assets
bash scripts/build_image.sh --target thor --version v0.40.3_20261012 --package-dir /path/to/release-assets
```

| Target | Runtime base | 默认飞书 sheet |
| --- | --- | --- |
| amd | Ubuntu 24.04 | AMD_with_cuda |
| arm | Ubuntu 24.04 | ARM_with_cuda、ARM_without_cuda、SOPHON_bm1688 |
| l4t | L4T JetPack R36.4.0 + JetPack 6 overlay | l4t |
| thor | CUDA 13.0.2 Ubuntu 24.04；不叠加 JetPack 6 | thor_spark |
| jetpack5 | L4T R35.4.1 + JetPack 5 overlay | 无默认 sheet；用 `--sheet-title` 指定 |

镜像仓库和组件列继续为 `swr.cn-southwest-2.myhuaweicloud.com/ictrek/ollama_server` / `ollama_server`。VOS 镜像 tag 保持 `amd_YYYYMMDD`、`arm_YYYYMMDD`、`l4t_YYYYMMDD`、`thor_YYYYMMDD`；二进制版本保留 `X.Y.Z_YYYYMMDD`。同平台同日期重发会覆盖该 tag。

`--no-publish` 只构建；`--dry-run` 只显示计划；`--sheet-title` 可重复指定。发布流程为 build → push → manifest 检查 → 写飞书 → 精确读回。固定 spreadsheet ID 为 `Htotsn3oahO1zxt73YMcaB1zn8e`；认证来自 `FEISHU_APP_ID/SECRET` 或构建主机 `~/.feishu.json`。CI 需 `SWR_USERNAME`、`SWR_PASSWORD`、`FEISHU_APP_ID`、`FEISHU_APP_SECRET` 四个 secrets。

真实模型验证入口：`python3 scripts/verify_gateway.py --binary /path/to/bin/ollama --work-dir /path/to/isolated-test`。它使用独立模型目录和 19134/19135 端口，测试 BGE/Qwen3 rerank、原生/OpenAI chat、completion、embedding、SSE 取消和共享 metrics；不会重启已有服务。GPU 驱动和加速行为仍需在对应硬件验证。
