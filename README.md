# storage-bot

飞书机器人，用于管理和诊断多套 Ceph 存储集群及非 Ceph REST 存储系统。通过自然语言（中英文均可）发送命令，机器人自动执行诊断、统计并用 AI 分析结果后回复。

## 功能概览

- **集群健康检查**：一键获取 ceph status / health detail / osd tree / df 并由 AI 汇总
- **集群巡检**：定时或手动一键体检（Ceph 状态 + 节点硬件），按节点分组的结构化报告，可推飞书卡片、Web 查看、落盘留存
- **日志分析**：SSH 登录节点收集系统日志，AI 识别异常并给出修复建议
- **节点诊断**：针对单个节点做磁盘、内存、进程、IO 诊断；kernel 日志、网卡列表、bond 链路状态
- **网口操作**：在确保 bond 冗余的前提下 up/down 单个物理网口（写操作需 `--yes` 确认）
- **磁盘 IO 统计**：对指定集群或单个节点执行 `iostat`，输出原始统计数据
- **Ceph Skill**：OSD / PG / Pool / 容量 / 慢请求 / Monitor / Crash / FSID / Mon IP / RGW PG 优化 / 重启 mon-mgr 等一键查询与操作
- **Flag 操作**：set/unset nobackfill、norebalance、norecover、noout，支持单套、前缀批量、全量（含排除）
- **REST 存储**：对接 Yanrong (yrfs) 云存储 REST API，支持集群信息、健康、配额列表、用户目录查询、回收站（列出/查询/清空，清空默认 dry-run）
- **Web 管理界面**：内置 HTTP 服务，可在浏览器查看已注册的集群、技能、节点、执行历史和巡检报告
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
  ├── inspect/           ← 集群巡检（Ceph + 硬件 inspector、调度、报告、留存）
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
| `list inspect` / `巡检项` | 列出巡检覆盖的所有检查项 |
| `cdn-01 健康状态` / `check cdn-01` | 集群健康检查 |
| `巡检 cdn-01` / `inspect cdn-01` / `体检 cdn-01` | 集群巡检（Ceph + 硬件） |
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
| `crash info` / `崩溃详情` | crash_info | 列出 crash 并展示最近一条完整信息 |
| `monitor` / `mon` / `仲裁` | mon_status | Monitor 仲裁状态 |
| `iostat` / `磁盘io` | io_stat | 节点磁盘 IO 统计 |
| `kernel` / `内核日志` | kernel_logs | 节点 kernel 日志（可带 `n=` 条数、`keyword=` 关键字） |
| `nic` / `网卡` | nic_info | 节点网卡列表（ip link） |
| `bond` / `链路聚合` | bond_status | bond 各 slave 的 Link Failure Count（非零标 ⚠） |
| `nic down` / `网口down` | nic_down | down 单个 bond 网口（写操作需 `--yes`；前置校验两口均 up，防双口断网） |
| `nic up` / `网口up` | nic_up | up 单个 bond 网口（写操作需 `--yes`） |
| `list nodes` / `节点列表` | list_nodes | 集群节点列表（名称 + IP）|
| `fsid` / `集群id` | get_fsid | 集群 FSID |
| `mon ip` / `mon地址` | get_mon_ips | Monitor IP 列表 |
| `optimize rgw` / `优化rgw pg` | optimize_rgw_pg | 优化 rgw.buckets.data 存储池 PG 分布（upmap） |
| `重启 mon` / `restart mon` | restart_mon | 重启指定 mon（删 pod，rook 重建；写操作需 `--yes`） |
| `重启 mgr` / `restart mgr` | restart_mgr | 重启指定 mgr（删 pod，rook 重建；写操作需 `--yes`） |
| `set/unset nobackfill` 等 | flag 操作 | 见下方「Flag 批量操作」 |

示例：
```
@bot cdn-01 osd状态
@bot 看看 hdd-01 的容量
@bot cdn-01 慢请求
@bot kernel cdn-01 bd-cdn-node02 keyword=link
@bot bond cdn-01 bd-cdn-node02
@bot nic down cdn-01 bd-cdn-node02 eth0 --yes
@bot 重启 mon a cdn-01 --yes
```

### 磁盘 IO

```
# 对集群所有节点执行 iostat
iostat cdn-01

# 对指定节点执行（节点名不存在时列出可用节点）
iostat cdn-01 bd-cdn-node02
```

### 节点诊断与网口操作

```
# kernel 日志（默认 200 条，已过滤 systemd/kubelet 等噪音）
kernel cdn-01 bd-cdn-node02
kernel cdn-01 bd-cdn-node02 n=500            # 指定条数
kernel cdn-01 bd-cdn-node02 keyword=link     # 指定关键字

# 网卡列表 / bond 链路状态
nic cdn-01 bd-cdn-node02                      # ip link 网卡列表
bond cdn-01 bd-cdn-node02                     # 各 slave 的 Link Failure Count

# up/down 单个 bond 网口（写操作，需 --yes 确认）
nic down cdn-01 bd-cdn-node02 eth0            # 预览，不执行
nic down cdn-01 bd-cdn-node02 eth0 --yes      # 执行 ip link set eth0 down
nic up   cdn-01 bd-cdn-node02 eth0 --yes      # 执行 ip link set eth0 up
```

> ⚠️ **网口 down 的安全约束**
> `nic down` 执行前会校验目标网口属于某个 bond，且该 bond 内所有成员口当前都是 up；
> 只有都 up 时才允许 down 其中一个，避免把两个口都 down 掉导致节点断网。`nic up`
> 同样要求网口属于 bond，避免误操作非 bond 物理口。

### Ceph 写操作 Skill

```
# 重启 mon / mgr（删除其 pod，rook 自动重建；写操作需 --yes）
重启 mon cdn-01                # 不带 id：列出候选
重启 mon a cdn-01             # 带 id：预览将删的 pod，不执行
重启 mon a cdn-01 --yes       # 执行
restart mgr b cdn-01 --yes

# 优化 rgw.buckets.data 存储池的 PG 分布（osdmaptool upmap）
optimize rgw cdn-01           # 默认 max=100
optimize rgw cdn-01 max=50
```

### 集群巡检

一键体检：Ceph 集群状态（health/osd/mon/pg/容量/慢请求/crash）+ 每个节点的硬件
（CPU load、内存、文件系统、磁盘 SMART、网卡/bond、NVMe/网卡 PCIe 链路）。结果按节点
分组、按严重级（🔴严重 / 🟡警告 / 🟢正常 / ⚪未知）汇总，可推飞书卡片、Web 查看、落盘留存。

```
# 手动巡检单套集群
巡检 cdn-01
inspect cdn-01
体检 cdn-01

# 巡检全部集群
巡检所有集群
inspect all clusters

# 查看巡检覆盖哪些检查项
巡检项
list inspect
```

定时巡检与推送在 `config.yaml` 的 `inspect` 段配置（见下方「配置参考」），到点自动跑、
有发现（达到 `notify_min_level`）才推送到指定飞书群。报告也可在 Web UI 的「集群巡检」
页手动触发和查看历史。

> 💡 **PCIe 链路降级**
> 巡检会比对每块 NVMe / 网卡的额定链路能力（LnkCap）与实际协商状态（LnkSta）：
> 宽度降级（掉 lane）→ 严重；仅速率降级 → 警告。同节点多块盘同样降级会合并成一条
> （`NVMe ×22 …`）避免刷屏。若集群是**故意**降速（如 PCIe 5.0→4.0），可在 thresholds
> 设 `pcie_min_speed_gts`（如 16），达标的纯速率降级视为预期、不告警。

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

# 回收站
yrfs01 recycles                                              # 列出全部回收站
yrfs01 recycle files /public-data/user/liangzheng            # 列出该路径下回收站文件
yrfs01 recycle clear /public-data/user/liangzheng/tmp        # dry-run，不真删
yrfs01 recycle clear /public-data/user/liangzheng/tmp --yes  # 真实清空
```

> ⚠️ **回收站真实清空的安全限制**
> `recycle clear ... --yes` 仅允许路径在 `public_user_prefix` 或 `private_user_prefix`
> 之下（即「个人目录」内）；其他路径会被后端拒绝并报错。Dry-run（不带 `--yes`）
> 不做此限制，可用于预览任意路径下将被删除的文件。
> 未配置任何 `*_user_prefix` 时，所有真实清空都会被拒绝。

也可以用 `yrfsctl` CLI 在终端直接调试，无需走飞书：

```bash
yrfsctl --config ./config.yaml --name yrfs01 info
yrfsctl --name yrfs01 quota --path /drtraining/user/liangzheng
yrfsctl --name yrfs01 quota --user liangzheng --scope private
yrfsctl --base-url https://10.0.0.5 --username admin --password 'pw' health

# 回收站
yrfsctl --name yrfs01 recycles                                                # 列出全部回收站
yrfsctl --name yrfs01 recycles --path /public-data/user/liangzheng            # 列出某路径下文件
yrfsctl --name yrfs01 recycle-clear --path /public-data/user/liangzheng/tmp   # dry-run
yrfsctl --name yrfs01 recycle-clear --path /public-data/user/liangzheng/tmp --yes  # 真实清空
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

inspect:                 # 集群巡检（可选）；enabled: false 时整套不启动
  enabled: false
  schedule: "0 3 * * *"          # 标准 5 字段 cron，每天 03:00
  clusters: []                    # 空 = 全部集群；或 ["cdn-01"]
  notify_chat: ""                 # 飞书 chat_id；有发现（≥ notify_min_level）才推送
  notify_min_level: warn          # warn | critical
  llm_summary: false              # 是否调 LLM 生成总结（复用 analyzer）
  history_dir: ./inspect-reports  # 历史报告目录
  history_keep: 30                # 每集群保留份数
  thresholds:                     # 留空用默认值
    capacity_warn_pct: 80
    capacity_crit_pct: 90
    mem_warn_pct: 90
    mem_crit_pct: 95
    fs_warn_pct: 85
    fs_crit_pct: 90
    disk_life_warn_pct: 80
    disk_life_crit_pct: 90
    load_warn_ratio: 2.0
    pcie_min_speed_gts: 16        # 0=禁用；>0 时纯速率降级且协商速率≥此值视为故意降级、静默
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
- **SSH 命令过滤**：拦截 `rm -rf`、`mkfs`、`shutdown` 等危险命令及 shell 注入；节点诊断只走 `lspci`/`smartctl`/`ip`/`grep` 等只读命令白名单
- **写操作二次确认**：重启 mon/mgr、up/down 网口等写操作默认只预览，需重发并加 `--yes` 才执行；`nic down` 还会前置校验 bond 冗余，避免双口同时 down 断网
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
├── skill/                # 内置 Skill（OSD / PG / IO / kernel / nic / bond / flag 等）
├── inspect/              # 集群巡检（Ceph + 硬件 inspector、调度、报告渲染、历史留存）
├── executor/             # kubectl exec（Ceph 命令）+ SSH（节点命令）
├── analyzer/             # LLM Provider（Claude / OpenAI / DeepSeek / 千问 / Ollama）
├── storage/              # REST 存储后端
├── web/                  # 内置 Web 管理界面（集群/技能/节点/巡检报告）
├── security/             # 命令白名单、SSH 过滤、LLM 输入消毒、审计日志
├── deploy/               # Kubernetes manifests + 多集群部署指南
└── config.yaml.example   # 配置文件模板
```
