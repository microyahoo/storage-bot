# storage-bot

飞书机器人，用于管理和诊断多套 Ceph 存储集群及非 Ceph REST 存储系统。通过自然语言（中英文均可）发送命令，机器人自动执行诊断、统计并用 AI 分析结果后回复。

## 功能概览

- **集群健康检查**：一键获取 ceph status / health detail / osd tree / df 并由 AI 汇总
- **日志分析**：SSH 登录节点收集系统日志，AI 识别异常并给出修复建议
- **节点诊断**：针对单个节点做磁盘、内存、进程、IO 诊断
- **磁盘 IO 统计**：对指定集群或单个节点执行 `iostat`，输出原始统计数据
- **Ceph Skill**：OSD / PG / Pool / 容量 / 慢请求 / Monitor / Crash / FSID / Mon IP 等一键查询
- **Flag 操作**：set/unset nobackfill、norebalance、norecover、noout，支持单套、前缀批量、全量（含排除）
- **REST 存储**：对接 Yanrong (yrfs) 云存储 REST API，支持集群信息、健康、配额列表、用户目录查询
- **Web 管理界面**：内置 HTTP 服务，可在浏览器查看已注册的集群、技能、节点和执行历史
- **yrfsctl CLI**：独立命令行工具，不经过 bot 直接调用 Yanrong API 做调试
- **配置热重载**：修改 config.yaml 或发送 SIGHUP，30 秒内自动生效，无需重启

## 架构

```
飞书消息
  │
  ▼
intent/parser.go      ← 规则解析（中英文关键词 + LLM 兜底）
  │
  ▼
bot/handler.go        ← 路由、集群查找、Skill 调度
  │
  ├── executor/kube.go   ← kubectl exec 进 rook-ceph-tools Pod 执行 ceph 命令
  ├── executor/ssh.go    ← SSH 直连或经 gateway 跳板收集节点信息
  ├── skill/builtin.go   ← 内置 Skill 实现
  ├── storage/           ← Yanrong (yrfs) REST 存储后端
  └── analyzer/          ← LLM 分析（Claude / OpenAI / DeepSeek / 千问 / Ollama）
```

`cmd/yrfsctl/` 提供独立 CLI 工具，复用 `storage.YanrongBackend`，可在不连飞书的情况下直接调试 Yanrong API。

## 快速开始

### 1. 创建飞书机器人

1. 进入[飞书开放平台](https://open.feishu.cn/) → 创建企业自建应用
2. 权限管理中开启：`im:message`、`im:message.receive_v1`
3. 事件订阅 → 添加事件：`接收消息 v2.0`
4. 连接方式选择 **长连接（WebSocket）**，记录 App ID 和 App Secret

### 2. 准备配置文件

```bash
cp config.yaml.example config.yaml
```

编辑 `config.yaml`：

```yaml
feishu:
  app_id: "cli_xxxxxxxxxxxx"
  app_secret: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

llm:
  provider: "claude"       # claude | openai | deepseek | qwen 
  api_key: "sk-ant-xxxxx"  # 也可以用环境变量 LLM_API_KEY

clusters:
  cdn-01:
    kubeconfig: "/etc/storage-bot/kubeconfigs/cdn-01"
    server_override: "https://10.0.1.10:6443"  # apiserver 可达地址
    gateway_node:
      name: "cdn-node01"
      host: "10.0.1.1:22"
      user: "root"
      key_file: "/root/.ssh/id_rsa"          # bot 本地的密钥，连 gateway 用
      gateway_key_file: "/root/.ssh/id_rsa"  # gateway 上的密钥路径，连其他节点用
```

### 3. 本地运行

```bash
make build-local
./bin/storage-bot --config config.yaml
```

### 4. Docker 运行

```bash
docker build -t storage-bot .
docker run -d \
  -v /path/to/config.yaml:/etc/storage-bot/config.yaml \
  -v /path/to/kubeconfigs:/etc/storage-bot/kubeconfigs \
  -v /root/.ssh:/etc/storage-bot/ssh:ro \
  -e LLM_API_KEY=sk-xxxxx \
  storage-bot
```

### 5. Kubernetes 部署

```bash
# 创建命名空间和密钥
kubectl apply -f deploy/manifests.yaml

# 填入 kubeconfig
kubectl -n storage-bot create configmap storage-bot-kubeconfigs \
  --from-file=cdn-01=cdn-01.kubeconfig \
  --from-file=hdd-01=hdd-01.kubeconfig

# 填入 SSH 私钥
kubectl -n storage-bot create secret generic storage-bot-ssh-keys \
  --from-file=id_rsa=/root/.ssh/id_rsa
```

详见 [deploy/MULTI_CLUSTER.md](deploy/MULTI_CLUSTER.md)。

## 使用方法

在飞书群里 @ 机器人，或在私聊中直接发送。支持中英文自然语言。

### 基本查询

| 说法 | 动作 |
|------|------|
| `帮助` / `help` / `?` | 显示使用指南 |
| `list clusters` / `有哪些集群` | 列出所有已配置集群 |
| `list skills` / `有哪些技能` | 列出所有可用 Skill |
| `cdn-01 健康状态` / `check cdn-01` | 集群健康检查 |
| `分析 cdn-01 的日志` | 日志收集 + AI 分析 |

### Skill 命令

直接在消息中说出 Skill 关键词 + 集群名：

| 关键词 | Skill | 描述 |
|--------|-------|------|
| `osd` / `osd状态` | osd_status | OSD 状态、down/out 列表 |
| `pg` | pg_status | PG 状态、unclean/inactive/stale |
| `pool` / `存储池` | pool_status | 存储池详情 |
| `容量` / `capacity` | capacity | 集群容量和各 OSD 使用率 |
| `慢请求` / `slow` | slow_ops | 慢请求和阻塞操作 |
| `crash` / `崩溃` | crash | Ceph 崩溃报告 |
| `monitor` / `mon` / `仲裁` | mon_status | Monitor 仲裁状态 |
| `iostat` / `磁盘io` | io_stat | 节点磁盘 IO 统计 |
| `list nodes` / `节点列表` | list_nodes | 集群节点列表（名称 + IP）|
| `fsid` / `集群id` | get_fsid | 集群 FSID |
| `mon ip` / `mon地址` | get_mon_ips | Monitor IP 列表 |

示例：
```
@bot cdn-01 osd状态
@bot 看看 hdd-01 的容量
@bot cdn-01 慢请求
```

### 磁盘 IO

```
# 对集群所有节点执行 iostat
iostat cdn-01

# 对指定节点执行（节点名不存在时列出可用节点）
iostat cdn-01 bd-cdn-node02
```

### Flag 批量操作

仅 `set/unset nobackfill`、`set/unset noout` 支持批量。

```
# 单套集群
set nobackfill cdn-01

# 前缀匹配（匹配所有含 "cdn" 的集群名）
set nobackfill cdn

# 全量
set nobackfill all
set nobackfill 所有

# 全量排除部分集群
set nobackfill all except cdn-test
set nobackfill 所有 排除 cdn-test cdn-staging

# 恢复
unset nobackfill all
unset noout cdn-01
```

### Yanrong (yrfs) 存储

在 `rest_storages` 中配置一个 Yanrong 存储后，可按存储名路由：

```
# 集群信息 / 健康
yrfs01
yrfs01 health

# 配额
yrfs01 quotas                              # 全部配额列表
yrfs01 usage /drtraining/user/liangzheng   # 精确路径

# 用户目录（用配置里的 private_user_prefix / public_user_prefix 拼接）
yrfs01 user liangzheng              # 默认 private
yrfs01 user liangzheng public
yrfs01 用户 liangzheng 公共
```

也可以用 `yrfsctl` CLI 在终端直接调试，无需走飞书：

```bash
yrfsctl --config ./config.yaml --name yrfs01 info
yrfsctl --name yrfs01 quota --path /drtraining/user/liangzheng
yrfsctl --name yrfs01 quota --user liangzheng --scope private
yrfsctl --base-url https://10.0.0.5 --username admin --password 'pw' health
```

凭据优先级：`--base-url/--username/--password` 标志 > `YR_BASE_URL / YR_USERNAME / YR_PASSWORD` 环境变量 > config.yaml 的 `rest_storages[--name]`。

## 配置参考

### 完整字段说明

```yaml
feishu:
  app_id: "cli_xxxx"
  app_secret: "xxxx"

llm:
  provider: "claude"     # 见下方"LLM 支持"
  api_key: "sk-xxx"      # 或环境变量 LLM_API_KEY
  model: ""              # 留空自动选择默认模型
  base_url: ""           # 自定义 API 地址（国内代理等）

dev:
  disable_llm: false     # true = 跳过 LLM，直接返回命令原始输出
  dry_run: false         # true = 不执行任何命令，仅显示将要做什么

clusters:
  <集群名>:
    kubeconfig: "/path/to/kubeconfig"
    namespace: "rook-ceph"             # 默认 rook-ceph
    server_override: "https://IP:6443" # 覆盖 kubeconfig 中的 apiserver 地址
    insecure_skip_tls_verify: false    # 跳过 TLS 验证（不推荐）

    # 节点访问方式二选一：
    # 方式 A：只配 gateway_node，其余节点由 kubectl get nodes 自动发现
    gateway_node:
      name: "node-1"
      host: "10.0.1.1:22"
      user: "root"
      key_file: "/root/.ssh/id_rsa"         # 本地→gateway 的私钥
      gateway_key_file: "/root/.ssh/id_rsa"  # gateway→其他节点的私钥

    # 方式 B：手动列出所有节点（覆盖自动发现）
    ssh_nodes:
      - name: "node-1"
        host: "10.0.1.1:22"
        user: "root"
        key_file: "/etc/storage-bot/ssh/id_rsa"

rest_storages:
  <存储名>:                            # 不能与 clusters 重名
    base_url: "https://10.0.100.5"     # Yanrong 管理面入口
    username: "admin"
    password: "xxxx"
    # 用户目录前缀，可选；用于将 `yrfs01 user liangzheng` 自动展开成完整路径
    public_user_prefix:  "/public-data/user"
    private_user_prefix: "/drtraining/user"

web:
  listen: ":8080"        # 留空则禁用 Web UI
  username: "admin"      # Basic Auth；留空则关闭鉴权（不建议）
  password: "xxxx"
```

### 环境变量

| 变量 | 说明 |
|------|------|
| `LLM_API_KEY` | LLM API 密钥，优先级高于配置文件 |
| `LLM_BASE_URL` | 自定义 API 地址，优先级高于配置文件 |
| `ANTHROPIC_API_KEY` | Claude API 密钥（当 LLM_API_KEY 未设置时生效） |
| `FEISHU_APP_ID` | 飞书 App ID |
| `FEISHU_APP_SECRET` | 飞书 App Secret |

### LLM 支持

| provider | 默认模型 | 说明 |
|----------|----------|------|
| `claude` | claude-sonnet-4-6-20250514 | Anthropic 原生 SDK；配了 `base_url` 自动切换为 OpenAI 兼容模式 |
| `openai` | gpt-4o | OpenAI API |
| `deepseek` | deepseek-chat | DeepSeek API |
| `qwen` / `dashscope` / `aliyun` | qwen-plus | 阿里云千问 DashScope |

**国内代理示例**（OpenAI 兼容接口）：
```yaml
llm:
  provider: claude       # 或 openai
  api_key: "sk-xxx"
  base_url: "https://your-proxy.example.com/v1"
  model: "claude-sonnet-4-6-20250514"
```

## 安全

- **Ceph 命令白名单**：只允许只读命令 + 明确列出的 flag 操作（nobackfill、norebalance、norecover、noout），拒绝一切破坏性命令（osd destroy、pool delete 等）
- **SSH 命令过滤**：拦截 `rm -rf`、`mkfs`、`shutdown` 等危险命令及 shell 注入
- **LLM 输入消毒**：用户消息在发往 LLM 前清除控制字符并截断至 2000 字符
- **操作审计**：每条操作记录用户 ID、集群、动作类型和执行状态（内存环形日志，最近 10000 条）

## 配置热重载

修改 `config.yaml` 后，文件 watcher 会在 30 秒内自动重载集群配置，不需要重启进程。也可以发送 SIGHUP 立即触发：

```bash
# 本地
kill -HUP $(pgrep storage-bot)

# Kubernetes
kubectl -n storage-bot exec deploy/storage-bot -- kill -HUP 1
```

热重载更新集群列表、SSH 节点配置以及 `rest_storages`（Yanrong）配置；LLM / 飞书等启动时参数不会变更。

## 多集群 kubeconfig 生成

详见 [deploy/MULTI_CLUSTER.md](deploy/MULTI_CLUSTER.md)，包含：

- 为每套集群创建最小权限 ServiceAccount 和 kubeconfig
- 处理 apiserver 地址和 TLS SAN 不匹配问题
- 将 kubeconfig 挂载进 Kubernetes Pod
- 无重启添加新集群

## 构建

```bash
# 本地构建
make build-local
# 输出：./bin/storage-bot

# Docker 镜像
make build

# 测试
make test
```

## 目录结构

```
.
├── main.go               # 入口：加载配置、初始化组件、启动飞书 WS 客户端
├── config/               # 配置结构体 + 文件 watcher（热重载）
├── intent/               # 消息意图解析（规则 + LLM 兜底）
├── bot/                  # 消息路由、Skill 调度、回复
├── cluster/              # 多集群管理（查找、前缀匹配、SSH 节点解析）
├── skill/                # 内置 Skill（OSD / PG / IO / Flag 等）
├── executor/             # kubectl exec（Ceph 命令）+ SSH（节点命令）
├── analyzer/             # LLM Provider（Claude / OpenAI / DeepSeek / 千问 / Ollama）
├── storage/              # REST 存储后端
├── security/             # 命令白名单、SSH 过滤、LLM 输入消毒、审计日志
├── deploy/               # Kubernetes manifests + 多集群部署指南
└── config.yaml.example   # 配置文件模板
```
