# 06 对话工作区与原生CLI

> 文档编号：06
> 项目：LoopForge
> 状态：历史文档整理（中文）
> 证据基线：`develop` @ `6833a9fb`
> 摘要：对话工作区、项目沙箱、仓库缓存、原生 CLI 会话续接与凭据注入。
> 迁移说明：本文件由以下原文档合并：`docs/conversation-workspaces.md`、`docs/conversation-native-cli.md`。
## 对话工作区与项目沙箱

普通对话中的 Main Bot 保持本地对话进程，需要看代码、改代码、跑命令、构建或访问 GitHub 时才调用 `workbench_workspace_open` 进入工作区模式。工作区是一个**运行环境**，不是一次检出：`workbench_workspace_open` 接收可选的 `sandboxProfileId` 与 `workspaceRunId`（其他参数一律报错），工具描述列出本项目和共享的可用沙箱（Docker，或执行机器的 native 轻量沙箱）以及项目登记的仓库地址供 clone 参考。不选环境时沿用当前工作区，还没有工作区时用会话设置、AI 助手设置里倾向的环境或平台 `generic-docker`。所选环境不可用（被删除、跨项目私有、Dockerfile 缺失）时返回可操作错误，由 Main Bot 改选，不会被静默替换。

传入 `workspaceRunId`（同项目的 SOP 运行 ID，或本对话之前开启过的可用工作区 ID；子 SOP 会自动定位到父工作区）时，要求目标工作区与当前对话在同一执行机器上（不在同一机器时如实报错并说明双方所在机器），直接将本对话绑定到该原工作区；已回收的快照会先原地恢复，且修改结果直接保留在原 SOP 工作区中，之后可直接在 SOP 上 `Retry` 重跑后续节点或 `runs_resume` 继续流程，删除普通对话也不会删除借用的 SOP 工作区。接入或进入 SOP 工作区与 SOP 自身的工作区编辑、终端、Retry、resume 一样需要项目写权限：本轮发言人是项目只读成员（viewer）时拒绝接入；共享对话已被其他成员绑定到 SOP 时，只读成员发言的那一轮以平台模式运行，不会继承该 SOP 工作区的写入能力。重新打开当前已绑定的工作区时若准备失败（租约被占用、执行机器暂时离线等），保留原绑定，稍后重试即可。执行机器比对使用运行实际所在的机器：没有冻结执行模式的旧运行按负责人默认执行机器计算。

工作区默认新开时是空目录，平台不预先检出任何仓库、不建分支。交接给 CLI 后，Agent 用自己的 git `clone`、`checkout`、建分支、`commit`、`push`，需要时 `merge`，并自己开 PR（`gh` 或 `curl` 调 API；镜像里没有 `gh` 时由 Agent 自行安装，或在沙箱配置里预装）。推送到主分支、强推、合并 PR 只在用户明确要求时做。

个人对话可直接使用共享沙箱开启可写工作区。Agent 可以创建文件、运行命令、预览，并把产物附到回复供查看和下载。有 Git 任务时，Agent 仍使用本轮发言人的凭据自行操作仓库。个人工作区只向会话所有者开放，Agent 只能接入当前对话开启的工作区。从已有个人工作区的对话选项目时，会在项目中新建对话，原个人对话和文件可继续使用。

用户可在「高级设置 → 显示运行环境选择」中为会话指定环境。会话 `sandboxProfileId` 非空时，它优先于 Main Bot 的选择，工具描述也会告诉 Main Bot 不要再选。

会话没有指定环境时，Main Bot 按「设置 → AI 助手 → 对话工作区」里本轮发言人倾向的沙箱类型（`workspaceDriver`：`docker`、`native`，留空由 Agent 选择）挑选：工具描述把这一类沙箱排在可选列表前面，并告诉 Main Bot 没有特殊要求时用其中第一个（Docker 优先 `generic-docker`）；Main Bot 不传 `sandboxProfileId` 新开工作区时也用它。任务需要的语言或工具链只有其他环境具备时，Main Bot 仍可以换。没有对应类型的可用沙箱时，退回默认的「拿不准用 generic-docker」。

项目会话在入口统一校验项目访问权限。所有具备访问权限的成员（包括 viewer）都拥有同一会话的完整操作权限，可以继续讨论、编辑用户消息、停止任意成员发起的回复、选择运行环境、使用工作区和浏览器工具，以及归档或删除普通项目会话。内部不再按会话创建者、本轮发起人或项目编辑角色二次限制。会话的存储归属保持稳定，工作区绑定和历史对所有参与者一致；模型调用、Git 推送与 PR 仍使用**当前这一轮发言人**的个人凭据。

### Git 凭据

工作区模式下的 CLI 进程（`runtime.BindGitWorkspace`）在每轮启动时按本轮发言人注入：

- HTTPS：个人配置的 Git Token。`GIT_ASKPASS` 指向工作区 `.agentflow/runtime/git-askpass-hosts`，脚本本身不含令牌，从提示里解析出 URL 主机，只在它恰好是 `https://github.com` 时以 `x-access-token` 应答；`github.com.example`、`github.com@example`、`http://` 等其他地址都拿不到凭据。带 `oauth2:`/`private:` 前缀的令牌不是 GitHub 令牌，不会注入。`GIT_TERMINAL_PROMPT=0`，不会卡在交互输入上。
- `GH_TOKEN`：同一个令牌，供 `gh` 或 `curl` 调 GitHub API。平台进程自身的 `GH_TOKEN`/`GITHUB_TOKEN` 不会透传进沙箱。
- SSH 克隆使用按 `host:port` 绑定的个人密钥。同一主机和端口的仓库共用该密钥。执行进程在内存中持有私钥，并通过临时 ssh-agent 签名。沙箱只接收 agent socket、公钥与 SSH 配置。平台内置 `github.com:22` 与 `ssh.github.com:443` 的主机公钥，其他主机由平台管理员在「平台管理 → Git SSH 主机信任」核验并登记。部署文件 `AGENTFLOW_GIT_SSH_KNOWN_HOSTS` 的条目与页面记录合并生效。Runner 0.11.1 及以上会在启动工作区会话时收到匹配的条目。同一主机和端口优先使用平台下发的受信公钥。平台仅下发吊销记录或没有匹配条目时，Runner 使用本地配置的受信公钥。两处的 `@revoked` 条目都生效。
- 提交身份：`GIT_CONFIG_SYSTEM` 设为发言人的登录名，邮箱 `<login>@users.noreply.loopforge.invalid`；Agent 可以用 `git config --global/--local` 覆盖。

工具结果和回复都会对发言人的令牌（含去掉前缀后的裸令牌）做脱敏；提示词要求 Agent 不打印、不回显、不写入令牌。

### 工作区的切换与生命周期

一个对话同一时间只绑定一个工作区（会话 `workspaceRunId`）。工作区按运行环境区分：不传 `sandboxProfileId` 或传的与当前工作区相同时继续用当前工作区；传了别的环境就切换过去——本对话之前在那个环境开过、仍可用的工作区（会话 `ownWorkspaceRunIds`）直接接着用，文件和已克隆的仓库都在，没有就新开一个空工作区。每轮上下文列出当前绑定的工作区、之前在其他环境开过的工作区，以及本条消息 @ 的需求和关联主需求各自有没有跑过 SOP、SOP 的工作分支；要接着 SOP 的改动，Agent 进入工作区后自己 clone 并 checkout 那个分支。

工作区只由 Main Bot 打开或切换：角色子 Agent 的 agent 文件用 `disallowedTools` 禁掉 `workbench_workspace_open`，网关在本轮还有 Agent 派发在跑时也会拒绝它。子 Agent 与 Main Bot 共用同一个已绑定的工作区；需要另一个环境时在汇报里说明，由 Main Bot 在派发结束后处理。

普通对话按轮次持有工作区租约：首次使用时取得，此后整轮工具调用复用并持续续租，不再逐条命令、文件操作或浏览器操作保存完整快照。正常流程只在两个时点保存：本轮结束（包括失败、取消、超时和异常收尾），或切换工作区前。切换与轮次结束会等待正在使用该工作区的工具完成；重新打开同一个工作区不触发保存。切换前保存失败（例如工作区超过快照上限或保存超时）不阻止切换：旧工作区照常解绑，文件原样保留，空闲回收在回收前会再次保存，保存成功之前不会回收；`workbench_workspace_open` 的结果用 `previousCheckpointError` 说明原因。轮次收尾时仍未解决的保存错误通过生成状态的 `workspaceWarning` 返回。续租遇到存储错误时在租约有效期内重试，只有租约已被他人取得或临近过期才中止本轮生成。部署的在途任务等待包含收尾保存；强杀或崩溃仍依赖保留的工作目录和既有空闲回收机制补偿。

切换完成后旧工作区解绑（`run_workspace_checkpoints.released_at`），空闲回收不再等一小时：下一次扫描保存兜底快照后回收，这次保存不在工具调用的返回路径上；切回或在 IDE 中再次使用会取消解绑。切回已回收的工作区时先恢复快照。SOP 仍在执行或暂停时不会被回收，它的生命周期仍由 SOP 自己决定；离开一个暂停中的 SOP 时，工具会提醒 Main Bot 它还没有继续。删除对话时，对话开过的所有工作区一并删除，借用的 SOP 工作区不受影响。

会话尚未关联需求时，消息里 @ 的需求（优先取跑过 SOP 的）自动成为关联主需求；在对话里派发需求（模型调用 `issues_dispatch`、自动草稿，或用户确认草稿，后者由聊天页通过 `X-AgentFlow-Chat-Session` 请求头带上会话）也会把该需求关联到会话。之后不 @ 需求的消息也会把它列给 Main Bot。SOP 总是准备自己的工作区，不再借用对话工作区。


已关联 SOP 的任务会话里，SOP 执行中工作区只读；Main Bot 用 `runs_pause` 或 `runs_cancel` 停下 SOP 后才进入工作区模式（`workbench_workspace_open` 返回 `handoff=false` 时会提示这一点）。SOP 的交付由它自己的交付节点完成。

对话工作区创建时冻结完整沙箱（连同它的 Dockerfile 和环境变量），之后不再改变环境；换环境就是切到另一个工作区。准备工作区失败时，本次新建的工作区会被取消并解绑，会话回到之前的工作区，不会卡在一个打不开的工作区上。

### Native 沙箱

会话工作区可以使用 native 沙箱（Linux 上解析为 bwrap，macOS 上解析为 seatbelt）。native 沙箱只有环境变量，没有安装脚本：命令使用执行机器上已安装的语言和工具，需要的设置（如 `JAVA_HOME`、代理）写进沙箱的环境变量。它不限定执行位置，用户的执行机器、云端 Runner 和平台本机都可以运行。

- **Linux (`bwrap`)**：与 Docker 布局一致：CLI 的 HOME 是 `<会话根>/home`，cwd 是 `<会话根>/ws`（挂的是运行工作区），审批钩子文件只读挂载；宿主文件系统其余部分只读，网络与宿主的 Docker 守护进程保持可达（可以在工作区里构建镜像、启动容器）。CLI 使用执行机器自己安装的版本。bwrap 需要创建 user/pid 命名空间并挂载 `/proc`，平台运行在默认权限的容器里（Compose 部署）时做不到：平台镜像不带 bwrap，即使装上，不开特权容器也建不了命名空间。Compose 部署请用 Docker 沙箱；要用 Linux native 沙箱，在原生安装的平台或执行机器（Runner）上运行。
- **macOS (`seatbelt`)**：macOS 没有每进程挂载命名空间，`seatbelt` 沙箱保留 HOME 为 `<会话根>/home`，工作区模式下 cwd 直接指向运行工作区目录（不重映射到 `<会话根>/ws`）。`seatbelt` 策略放开运行工作区、临时目录与 `<会话根>/home` 写入，并在最后追加审批钩子及其父目录的 `file-write*` 拒绝规则以防止篡改或改名替换；宿主其余路径保持只读，回环端口（工具中继、本机浏览器 CDP）与本机网络/Docker 保持可达；未显式设置 `DOCKER_HOST` / `DOCKER_CONFIG` 时自动解析宿主当前 Docker context 端点注入 `DOCKER_HOST`，以便在隔离 HOME 下使用 Colima、OrbStack 或 Docker Desktop 等守护进程。CLI 使用执行机器自己安装的版本。

Docker 与 bwrap 下两种模式的 CLI 都在 `<会话根>/ws` 运行（工作区模式下这里挂的是运行工作区），按 cwd 存放原生会话的 CLI（如 Claude Code、CodeBuddy）进出工作区、换环境都能续接。seatbelt 下由于平台模式与工作区模式的 cwd 不同，或任一环境原生日志确实不可用时，平台在当前阶段用完整对话上下文新建原生会话继续（`cli.resume_fallback`），每个阶段最多一次，并告诉新会话当前所处的模式。

### 仓库缓存

执行机器（Runner 或平台本机）按仓库记录 ID 维护宿主侧的私有裸仓库缓存。native 沙箱内的 SOP 检出先用本次检出的 Git 凭据按仓库加锁刷新，再从本机缓存复制 Git 对象到工作区，将 origin 指回仓库地址。缓存不可用时会直接从远端克隆。每个工作区和快照各自保存 Git 对象，删除工作区后缓存仍可用于后续检出。Runner 0.12.0 起支持。Docker 沙箱照旧直接从远端克隆。

普通对话工作区从空目录开始，Agent 使用当前发言人的 Git 凭据克隆所需仓库。自动选择沙箱时会考虑执行机器的可用类型。平台和 Runner 0.12.0 通过实际启动短命令确认系统轻量隔离是否可用。会话明确指定的沙箱仍按指定配置打开。

local 沙箱类型已移除。`migrations/041_native_sandbox_inline_assets.sql` 把现有 local 沙箱改成 native，移除运行机器、基础目录和仓库路径的绑定，并复制引用环境中的普通变量。迁移删除环境安装脚本和 native 沙箱变量的 `PATH`，将依赖旧默认沙箱的 SOP 改到 `generic-docker`。已有项目若使用了这个保留 ID，迁移会为该沙箱分配新 ID，同步更新显式选用它的 SOP、会话选项和对应的工作区快照。现有部署在启动新版本前手动执行此迁移。尚未迁移的 local 行读出时按 native 处理；冻结了 local 沙箱的旧运行会提示改用 native 后重新发起。

`PUT /api/assistant/sessions/{id}/sandbox` 接受 `{ "sandboxProfileId": "id 或空字符串" }`，具备项目访问权限的会话参与者均可调用；生成期间和任务会话拒绝变更。已有工作区不受影响，新选择只用于之后新开的工作区。普通会话创建时也可以提交 `sandboxProfileId`，后续普通会话保存不能覆盖已保存的选择或伪造工作区绑定。

对话工作区使用独立的 `Run.kind=workspace` 和 `conversation-workspace` 标识，不参与 SOP 调度或占用 SOP 配额；继续复用 Runner、沙箱、Git、终端、快照、租约和空闲回收。API 与 Runner 需一起升级，旧 Runner 对新工作区请求会收到明确的版本要求。

旧群协作的邀请、角色派发、群 MCP、清单、自动跟进及切回 SOP 写入口退出，对话只有一种状态。手动 SQL 把会话 workspaceRunId 指向的历史临时工作区转为 workspace；其余未结束的群运行连同节点与 Runner 任务一并置为 cancelled，其群会话成为已结束任务的普通任务会话，历史消息与交接材料保留。已取消的群记录没有可续跑的 SOP 图，run 控制接口拒绝对它重试或恢复。

部署时先停旧 API/Worker，手动执行 `migrations/020_conversation_workspaces.sql`，然后启动新版 API/Worker/Runner。不删除历史数据，也无需为项目配置对话环境。生产启动不执行数据迁移或 seed。

### 需求关联与统计

要交给用户的文件（HTML demo、报告、JSON/CSV 等文本文件）用 `workbench_publish_artifact { path }` 发布：附在本轮回复里，HTML 在产物栏可交互预览。空间会话里不带 `path` 时照旧把 `body`/`imageBase64`/`url` 发布到空间侧边栏。原来的 `workbench_workspace_attach` 已并入这个工具，只在角色交付物收集里作为内部调用保留。

项目对话里直接改代码也要落到需求上。Agent 不需要调用登记工具：每轮回复保存前，平台列出工作区里两层目录内的 git 检出（远程执行机器上由 Runner 扫描，旧版 Runner 不支持时跳过），对其中指向 github.com 的远程仓库，用本轮发言人的 Git Token 查最近创建的 PR，满足以下全部条件的才登记：作者是该 Token 的用户、创建时间晚于工作区创建、头提交是工作区某个本地分支的当前提交、未关闭（或已合入），且还没有任何需求跟踪它。这样同一用户在别的会话或浏览器里开的 PR 不会被误登记；Agent 推送后又在本地追加了未推送的提交时，本轮不登记，下次推送后再登记。登记成功时回复末尾附一句登记结果。工作区仍未关联需求时，优先复用会话已关联的未完成需求，否则以第一个 PR 的标题和描述创建；借用已结束 SOP 工作区时登记到该 SOP 的需求。新建需求为本地 `requirement`、`in_progress`、`automationState=ignored`（不会被自动派发 SOP），负责人为当前用户，记录来源会话、工作区仓库和创建操作，并同时关联工作区与会话。已关联需求的工作区不再改关联；SOP 运行的需求只在派发时确定。

登记的 PR（记为 `github:owner/repo`）写入该需求的交付 MR 并登记合入跟踪，链接能对上项目登记的仓库时同时记录仓库，需求转为 `in_review`；PR 合入默认分支后由轮询把需求标为完成。每轮对话的模型用量按回复模型定价累加到当前对话工作区。

数据统计中，对话工作区不是 Run：不计入运行数、成功率、耗时和「平均 Token / Run」，但其 Token 与成本计入总量、趋势、成员、项目和来源，并在 `summary.conversation*` 与趋势点 `conversationTokens/conversationCost*` 中单列。只经对话改代码的需求（`directDelivery`）在交付明细中按 MR 状态显示「对话改代码中 / 对话 · MR 待合入 / 对话 · MR 已关闭」，MR 合入默认分支后算已交付并显示「对话直接交付」；同一需求有 SOP 运行时仍按 SOP 口径。对话工作区不出现在「运行记录」列表、运行概览和全局搜索中，也不计入 SOP 页「进行中 / 待审批」：它没有节点，且在回收前一直是 waiting，列出来只会变成看不懂的「已回收」记录。关联了需求的对话工作区仍在需求列表「关联 SOP」中显示为「对话工作区」。入口是对话本身（侧栏工作区）和关联需求；从对话直接打开 `#/runs/{id}` 仍可查看工作区详情。

### 界面与样式

环境选择默认隐藏，普通用户由 Main Bot 自动选择环境。技术人员通过「＋ → 高级设置 → 显示运行环境选择」开启；显示偏好按用户保存在当前浏览器，隐藏入口不改变已选环境或已有工作区。开启后，环境选择与项目选择并排，复用同一个 `SelectMenu`，使用相同的搜索、选中态和弹出方向规则。菜单按 Docker、native 分组排列，每项下方注明沙箱类型。菜单底部的「新增沙箱」打开当前项目的创建页，并默认选择 Docker；返回工作台后重新读取环境目录。普通对话随时可以改选环境，只影响之后新开的工作区；任务会话只展示任务环境。

旧协作样式中的全站主题、顶栏和页面框架规则归入 `web/app/frame.css`；仍由 SOP、角色等页面使用的模型控件样式归入 `web/assistant-models.css`。移除旧 UI 不改变其他页面的背景、搜索框或导航。

### 本地验证

- 原生对话前端 178 项测试通过；后端 HTTP、Engine、Runner、Repo、MCP 与 Web/OpenAPI 契约回归通过。
- 独立 Docker MySQL 验证了会话环境与生成启动的并发事务、SOP 配额，以及迁移脚本的作用范围与幂等性。
- 真实 CodeBuddy 对话分别选择项目默认和会话覆盖 Docker 环境，命令读取到 `business-default` 与 `conversation-override`，文件写入与验证命令均返回 0。新增沙箱入口也已通过实际界面创建验证。
- 全量测试尚有独立于本次修改的既有问题：MySQL 分析统计覆盖用例在未修改的 master 同样失败；macOS 的 Linux 专用 seatbelt 断言及缺少 flock/Unix socket 路径限制影响全量执行。CMD 与 deploy 用例已另在 Linux Docker 中通过；运行时整包的 Docker 回收/发现测试失败也已在未修改的 master 复现。

#### 会话与协作 Token 统计

「统计 → 用量 → Vibe Coding 与协作」展示会话 Token 和协作会话 Token。数据来自普通会话中已经保存的助手回复，不要求创建工作区；SOP 讨论会话不计入。协作沿用当前会话中至少两位用户实际发言的口径，协作 Token 是会话 Token 的子集。

Token 按回复时间归入日、周、月，独立于会话数的创建时间。没有时间的旧回复只计入累计用量。总量沿用 `usage.totalTokens`，未提供总量时按输入 + 输出计算，缓存读写分别展示；缺少 `usage` 的回复标为未采集，不视为零消耗。会话分支复制的历史前缀不重复计费。

个人范围显示本人创建或参与的会话整体用量，不代表个人账单。此会话用量视图与已有工作区用量可能重叠，不再加入 Run 总投入。统计基于当前保留的回复，已删除或改写的历史、未保存的失败生成不推算用量。

### 需求列表中的对话需求

只在对话工作区里做、没有派发过 SOP 的需求（没有 Task，关联运行全是对话工作区；工作区记录已不在时以需求的 `conversationSessionId` 为准）不按 SOP 节点算状态，而按交付推进：未提 MR 为「对话开发中」，MR 未合入为「MR 待合入」，直推共享分支（无 MR、`in_review`）为「已直推 · 待验收」，MR 全部关闭为「MR 已关闭」，合入后与 SOP 需求一致。进度列显示阶段文字而不是节点百分比；工作区被取消（会话删除后）不算失败。「查看」打开需求详情的「执行进展」，展示对话交付：交付 MR 与状态、工作区检出的仓库与工作分支、「打开会话」。「打开会话」对有权限的项目成员都显示（非创建者以只读打开），会话删除后显示「对话已删除」；是否已删除由 `GET /api/issues/{id}/requirement` 的 `conversation { sessionId, available, branches }` 给出。需求详情不提供进入对话工作区运行、日志或 SOP 操作的入口。

### 删除会话

删除会话（手动删除、定时会话过期清理、删除定时任务或新一次定时运行替换旧会话）都经 `assistantHandlers.deleteConversation`，按它打开过的工作区和关联的需求逐个处理（一个会话可以做多个需求）：

- 已交付的需求（登记过 MR，无论开启、合入还是关闭；直推过共享分支而处于 `in_review`；或已完成）保留。它的对话工作区只结束（取消并回收沙箱），记录保留用于统计对话用量与交付；它不出现在任何列表，需求详情也不链到它。
- 未交付的需求：对话工作区照旧取消并删除；若需求由本会话创建（`conversationSessionId`）且从未派发过 SOP（没有 Task，关联运行全是对话工作区），需求一并删除。
- TAPD、需求列表新建或派发过 SOP 的需求不会被删除；仍有任务在执行、已暂停或排队的工作区留给运行页处理。

## 会话执行机器与原生工作区

发送首条消息、实际开始生成时固定执行机器，之后不能修改。发送前选择机器只保存在本地草稿，不创建空会话；机器列表刷新保留草稿选择，个人默认机器未加载时不能首发。历史上因选择机器产生的空草稿可以重新选择，也不常驻会话侧栏。未记录执行机器的存量会话归属云端，不使用用户后来修改的默认机器。新会话选择远程 Runner 后，平台模式、工作区模式、标题生成和独立审批模型都由该 Runner 执行；机器不可用时返回错误，等待原机器恢复。

平台模式保留平台管理工具，并限制原生写入和命令。用 `workbench_workspace_open` 选一个运行环境（Docker 或 native：Linux bwrap / macOS seatbelt 沙箱）打开工作区后，当前 CLI 阶段退出并保存原生日志；平台轮换工具令牌，在同一机器上续接原生会话重新启动 CLI，启用原生文件、补丁与命令工具。Docker 与 bwrap 模式下 cwd 都是 `<会话根>/ws`（工作区模式下它挂的就是运行工作区），按 cwd 归档会话的 CLI（Claude Code、CodeBuddy 等）跨模式、换运行环境都能续接。macOS seatbelt 没有每进程挂载命名空间，工作区模式保持 HOME 为 `<会话根>/home`（审批钩子与父目录在沙箱规则中写保护，宿主机其余路径只读），cwd 直接指向运行工作区路径。续接仍然失败时（包括 seatbelt 跨模式 cwd 变化，或原生日志丢失、被服务商拒绝），每个阶段可以用完整对话上下文新建一次原生会话，模式切换后的新会话会被告知已处于哪个模式，不会为重做切换再切一次。Docker 工作区里 CLI 复用工作区现有容器，浏览器与准备脚本使用相同环境；native 工作区使用执行机器上已安装的 CLI。工作区模式的 CLI 按本轮发言人注入 Git Token（只对 github.com 应答的 askpass）、`GH_TOKEN` 与按主机绑定的 SSH 密钥，git 克隆、提交、推送和开 PR 都由 Agent 自己完成，详见 conversation-workspaces.md 的「Git 凭据」。

工作区只提供模式切换、产物、浏览器及任务明确选择的 MCP 集成；开出的 GitHub PR 由平台在每轮结束时自动登记，不需要工具。平台管理和旧的文件操作工具既不出现在目录中，也不能通过直接调用执行。模式切换令牌在旧进程退出后更新，旧令牌失效；交接期间拒绝继续调用工具。切回平台模式保留工作区和原生会话，关闭文件侧栏只改变界面。

模式切换由 Agent 按任务自动处理，界面不提供切换入口；内部控制静默完成；交接阶段的模型文字不写入展示流，不要求用户理解或确认进程重启。沙箱内的原生开发操作不产生审批卡，只有写入沙箱外系统的命令按审批模式处理（见下文）。

原生 HOME 和工作目录路径在模式间保持稳定。配置、技能、可执行资产与二进制附件由平台准备；原生会话日志保留在执行机器，错误或取消后继续原会话。远程 Runner 通过已认证的控制连接同步资产、运行 CLI 和转发会话工具请求，不在持久任务行中保存进程输入或密钥。

会话支持 PNG、JPEG、WebP 和 GIF 图片输入。CodeBuddy、Codex、Claude Code、OpenCode、Pi 和 ZCode 在当前用户消息中接收原生图片。ZCode 通过 `--attach` 传图，已验证 CLI 0.17.1。Kimi Code 用 `ReadMediaFile`，DeepSeek Harness 用 `read_image` 读取会话目录中的附件。Claude Code 使用 Runner 登录时，如果内嵌图片的消息超过桥接输入帧上限，会通过 `Read` 读取附件路径。识别需要所选模型支持视觉输入，或由 CLI 提供识图能力。续聊只内嵌当轮新增的图片，历史附件仍保留在会话目录中。远程 OpenCode、Pi、Kimi Code 和 DeepSeek Harness 的自定义模型配置由 Runner 生成，使用这些 CLI 传图时需要更新 Runner。

ZCode 恢复时通过官方会话协议应用当前选择的模型，再启动原生 prompt 命令；保留同一 session 和历史，不直接改写 CLI 数据库。协议不兼容或模型不可用时中止，保留日志供重试。

工作区是独立沙箱，沙箱内的原生文件、补丁和命令（编辑、构建、测试等）在三种审批模式下都直接执行，不做人工确认或独立模型审查。越过沙箱、写入外部系统的命令才按审批模式处理：`git push`；`gh pr|issue|release|repo|… create/merge/close/comment/edit/review/delete…`；`gh api` 带写方法或 `-f/-F/--input`；`curl` 带 `-X POST/PUT/PATCH/DELETE`、`-d/--data*`、`-F/--form`、`-T`、`--json`，`wget --post-*`；`npm/pnpm/yarn/bun/poetry/cargo publish`、`twine upload`、`gem push`；`docker/podman push`；`kubectl apply/delete/…`、`helm install/upgrade/…`、`terraform apply/destroy`。识别看工具输入里的 `command/cmd/args/script` 等字段，与 CLI 的工具名无关。这是防误操作的护栏，不是安全边界：脚本自己联网写入不会被识别。

- 完全权限：直接执行。
- 帮我批准：只有批准和拒绝两种结果，不出审批卡，也不转人工批准。hook 第一次轮询时平台在后台启动独立审查模型，按本次命令和对话授权判断（审查预算 60 秒，每次 hook 请求最多等 10 秒，没出结果就返回 pending 继续轮询，同一调用只审查一次）；通过即执行，审查拒绝、审查不可用或超时都直接拒绝，Agent 收到原因（「操作未执行，不要换一种方式重试」）并告诉用户。
- 请求批准：命令暂停，对话里出现「批准外部写入」卡片（显示识别出的类型与完整命令），只有本轮发言人能批准或拒绝；批准只对这一次调用有效。拒绝后 Agent 收到「用户拒绝了本次外部写入」，提示词要求它不换方式重试。停止回复、CLI 退出或模式交接时卡片失效；等待超过 9 分钟由平台自动拒绝（Kimi 的 hook 上限 600 秒，超时会放行，hook 自身也在 570 秒时拒绝）。

批准状态记在本轮 generation 的 payload 里（`nativeApprovals`），任一 API 实例都能读写，不新增表。卡片通过 `GET /api/assistant/sessions/{id}/native-approvals` 每秒轮询，决定走 `POST …/native-approvals/{approvalId}`。平台配置写入沿用草稿审批：请求批准下提交草稿由用户确认，帮我批准经独立审查，完全权限直接执行。外部 MCP 写入指对话选中的、经平台网关代理的第三方 MCP 服务器（如 TAPD、自定义 MCP）的写工具，在 `agent_tool_mcp.go` 的网关分发处按读写分类处理：完全权限直接执行；帮我批准经独立审查，未通过即拒绝；请求批准下在同一轮显示「批准外部写入」卡片，包含 MCP 服务、工具和脱敏后的实际参数。本轮发言人批准时，网关执行该次调用并把结果返回给 CLI。拒绝、停止、切换模式或调用超时后，待批准的写入失效。系统最多等 9 分钟，CLI 可提前结束等待：CLI 断开请求，或按 MCP 规范发送 `notifications/cancelled`（例如它自己的工具调用超时），网关都会取消该次调用，卡片随之失效，之后再批准也不会写入；CLI 重试会生成新的卡片。Kimi 的 MCP 工具超时默认 60 秒，平台写入的 `mcp.json` 为每个服务配置 `toolTimeoutMs: 600000`（OpenCode、DeepSeek 同为 600 秒）。卡片参数按字段名（归一化后匹配 `*token`、`*secret`、`*password`、`*apikey`、`*credential(s)`、`authorization`、`cookie` 等后缀，`max_tokens` 这类计数不算）和常见密钥格式（Bearer、GitHub/GitLab/Slack/OpenAI/AWS 密钥、JWT、PEM 私钥）脱敏；`headers`、`credentials` 等对象只隐藏其中的密钥字段，其余内容照常显示，标题会注明「N 个字段已隐藏」及字段路径。远程执行机器需要支持取消单次 MCP 请求的 Runner。推送主分支、强推和合并 PR 另由提示词约束为只在用户明确要求时做。开启工作区与切回平台模式本身不触发审批。各 CLI 的 hook 同时检查本轮是否仍有效及平台模式限制；取消、CLI 退出或模式交接后拒绝旧阶段调用。平台网关连续 120 秒不可用时 hook 拒绝本次操作（等待批准期间网关可达，不计入），不等待 CLI 自身的 hook 超时（部分 CLI 超时即放行）；Kimi 使用其原生 hook 配置，hook 的非 2 退出码统一转为拒绝。配置与 hook 文件在 Docker、bwrap 中只读挂载，在 macOS seatbelt 中同样保持只读保护。

Runner 需要 0.10.1 或以上版本，该版本支持取消后台导出并释放工作区锁。Docker 镜像优先使用已安装的 CLI；支持官方安装器的 CLI 缺失时自动安装。ZCode 没有独立 npm 包，须在执行机器及工作区镜像内预装官方 CLI 并加入 PATH，缺失时明确报错。原生工具调用围栏与进程生命周期管理依赖 Python3。每个 Docker 会话阶段使用独立的 Linux 子进程收养进程，结束或取消时回收其全部后代，包含另建进程组的原生命令；不影响容器里的其他命令。平台会在启动前检查依赖，固定安装步骤以容器 root 用户在 `/` 中运行，实际 CLI 保留工作区的容器用户；无法准备时中止，不降级绕过调用围栏。建议在受控工作区镜像中预安装 CLI 和依赖。

个人执行机器可为 Codex、Claude Code 和 Antigravity 选择「账号登录」，对话按所绑定机器的认证设置启动 CLI。在运行 Runner 的系统账户下登录后，Codex 读取 `CODEX_HOME` 下的 `auth.json`，Claude Code 读取 `CLAUDE_CONFIG_DIR` 下的 `.credentials.json` 或 Runner 环境中的 `CLAUDE_CODE_OAUTH_TOKEN`。Codex、Claude Code 仅存于系统钥匙串的登录需要改用 CLI 支持的文件式登录或 API Key；Antigravity 的凭据库支持见下文。

登录状态仅在该执行机器上使用。每次运行把登录文件复制进隔离的 CLI HOME，结束后删除；CLI 刷新 token 后，Runner 在源内容未改变时保存回原来源。当前格式与账号字段比较属于本地检查，不能证明整套 token 的服务端归属；完整的写回归属验证另行加固。设置页在 CLI 行的「更多」菜单提供「测试」，按当前认证方式在该机器上发送一次短模型请求。

SOP 的 native 和 Docker 沙箱也使用所选 Runner 的认证设置。每次 CLI 调用都在节点的独立 HOME 中使用临时登录文件，结束后清理该文件，会话可在同一节点续接。

Runner 0.10.2 起，机器卡片可检查并手动安装平台提供的 Runner 构建。更新在机器空闲时应用。自行安装的 CLI 如提供非交互更新命令，可以在 CLI 菜单中直接执行更新。托管安装使用托管更新。如果 CLI 无法自更新，界面会提示按原安装方式更新。

CLI 菜单中的「模型列表」与个人配置共用当前用户的模型目录。可以获取、筛选、添加、重命名和排序模型。保存后，AI 对话和角色编辑器使用该目录。API Key 模式通过个人配置里的 Key 与服务地址获取模型，留空地址时使用 CLI 的官方服务。Codex、Claude Code 和 Antigravity 的账号登录模式从所选机器查询。

Antigravity 的账号登录读取运行 Runner 的系统账号已有的凭据库或远程会话认证文件。对话工作区和 SOP 的 native、Docker 沙箱使用私有 HOME 中的认证副本，由官方 CLI 自动刷新，结束时保存回原来源并清理副本。Docker 镜像需要安装官方 agy CLI。共享对话中，只有机器所有者发言时使用该登录。实测版本为 agy 1.5.10。

其他 CLI 使用个人配置中保存的 API Key。Antigravity 也可切换为 API Key，读取个人配置里的 Gemini API Key，用量按 Gemini API 单独计费。详见 [Antigravity CLI 认证](https://antigravity.google/docs/install?tab=cli)和 [Gemini API 计费](https://ai.google.dev/gemini-api/docs/billing)。

ZCode 可通过 GLM Coding Plan 的 API Key 和套餐专用端点使用订阅额度。Start Plan 的额度需要登录 ZCode 账号，无法通过 API Key 使用。详见 [ZCode 模型配置](https://zcode.z.ai/cn/docs/configuration)。

原生会话的工具回调在执行机器上中继。宿主进程使用 Docker host gateway；通过 docker.sock 管理工作区的容器进程自动识别自身容器网络，工作区加入相同网络并访问该地址。自定义路由可通过 `AGENTFLOW_CONVERSATION_RELAY_HOST` 和 `AGENTFLOW_CONVERSATION_DOCKER_NETWORK` 显式指定工作区能访问的执行进程地址与 Docker 网络。会话目录会以相同路径挂进工作区，而 docker.sock 背后的 Docker 守护进程按自己所在主机解析挂载源，因此容器内的平台进程须把 `AGENTFLOW_CONVERSATION_DIR` 设在与宿主机同路径挂载的目录中；`compose.yaml` 将其设为 `${LOOPFORGE_DATA_DIR}/conversations`。Runner 不读该变量，原生会话目录固定在其工作区根目录的 `.conversations` 下，容器内运行 Runner 时须同路径挂载该工作区目录。未设置时会话目录位于 `AGENTFLOW_ASSISTANT_SESSION_DIR` 下，该目录在命名卷里时打开工作区会报 `mkdirat ... read-only file system`。附件等按用户保存的助手数据仍在 `AGENTFLOW_ASSISTANT_SESSION_DIR`，不挂进工作区。Docker 工作区直接在主容器中运行 SOP 脚本和 CLI。历史容器存在指令遮蔽挂载时，工作区下次取得执行权后会重建容器并保留工作区文件。标记为重建时运行的准备脚本随后在新容器补跑。

模式交接时，交接工具要求模型只回复一句「好的。」再结束旧阶段，交接之后的展示由平台隐藏；不要求“不输出任何内容”，否则模型可能只产出推理、没有正文的回合，该回合写入原生日志后，续接时会被模型服务拒绝（如 `the conversation must end with a user message`）。交接工具成功后，旧阶段即使未输出 `type=result` 或异常退出，也继续完成交接。续接原生会话时，如果原生日志缺失，或模型服务在本阶段产生任何输出或工具调用之前就拒绝了续接（400/422、会话结构无效、无结果退出），平台用完整对话上下文新建一次原生会话重试；已有输出或工具调用的中断，以及鉴权、限流、额度、网络、执行机器不可用等原因，仍续接原会话或直接报错，不重复执行副作用。

执行机器与运行模式记录在会话 payload 中，存量会话无需回填 SQL；不新增审批表或迁移。

停止先写入持久取消状态，并立即通知本机执行上下文；跨实例通过心跳转发。立即中断原生进程及其后代，不展示「正在停止」中间态；确认退出后结束本轮并恢复发送。启动前分配进程编号，取消同时阻止迟到的启动请求；停止响应丢失时重试并确认实际退出，不把 RPC 超时当成成功退出；执行机器长时间不可达时，停止重试最多持续 30 秒，之后结束本轮并在诊断中记录 `cli.stop_abandoned`，由执行机器在控制连接或会话租约结束时回收进程。执行机器的请求槽位已满（429）时，平台按指数退避重发本次请求（共约 6 秒），不直接判定本轮失败。完成或取消时只将工作区租约移交后台存档，不等待压缩上传；下一轮可抢占存档，旧存档通过 token CAS 禁止覆盖新快照或释放新租约。后台失败保留原文件，记录补存重试，进程中断留下的待保存状态也由维护任务恢复。

### 本轮验收（2026-11-13）

每轮实际部署前均由独立子 Agent Review，修复确认的问题后再部署。在独立 Docker MySQL、双 API 与远程 Runner 中，CodeBuddy 使用 CLI 默认模型完成云端和远程 Runner 的原生文件创建、编辑、命令读回，以及「帮我批准」模式下的原生命令；原生日志确认该默认模型为 Claude-Sonnet-5。全程无原生操作审批卡。

实际测试发现数字容器 UID 缺少系统用户记录时，CodeBuddy 的 `os.userInfo()` 会失败。现已在受控准备中读取实际 UID/GID，仅为缺失身份追加禁登录账户，使用稳定 CLI HOME；已有身份和容器执行用户保持不变。修复后，非 root 服务使用默认 root 镜像的云端模型测试通过。Debian 与 Alpine 的缺失/已有 UID、只读两种组合、重复准备及容器防护回归均通过；只读镜像缺身份时明确提示预置账户。

| 场景 | 模型 | 取消请求返回 | 确认本轮结束 |
| --- | --- | --- | --- |
| 远程 Runner，另一 API 取消 | CodeBuddy/CLI 默认模型 | 27 ms | 443 ms |
| 远程 Runner，另一 API 取消 | ZCode/DeepSeek 官方 | 26 ms | 404 ms |

两种 CLI 均在真实原生命令创建独立进程组后停止，立即续聊写入，跨过延迟写入时点后确认无残留文件。ZCode 临时凭证清零，原生 session ID、日志与续聊保留。云端 Chrome 实测停止后恢复发送界面，无「正在停止」文案；60 秒延迟子进程未写入，原会话续聊成功。

移除界面模式切换入口后，浏览器确认右上角无切换按钮、审批模式显示「请求批准」。CodeBuddy/Hy4 在远程 Runner 上完成自动往返：工作区原生写入后，查询平台角色时由 Agent 自动切回平台，再要求追加文件时自动回到同一工作区并核对两行内容；执行机器与工作区不变，回复不出现内部交接说明。显式选择 Hy4 的云端原生写入、编辑、命令读回及「帮我批准」模式同样通过。

前端 52 个文件、353 项测试及生产构建通过；Repo、HTTP API、Runner 全包通过。Engine 在本机低并行下通过，已排除已知基线用例 `TestControlPlaneCancelRequestsRunningWorker`，未记为全量无失败。相关 runtime race、Linux 子进程、启动取消专项及 MySQL 并发建会话回归通过。MySQL 回归同步缺失行的锁竞争，验证 1213 死锁后仅重试完整数据库事务；其他连接/提交错误不重放。

### 历史验收记录（逐项原生审批调整前，2026-11-13）

本地使用独立 Docker MySQL、API、远程 Runner 和预装 CLI 的 Docker 镜像验证，未修改线上数据库。本改动同时包含会话执行机器选择功能，已有会话归属云端；新会话发送首条消息、实际开始生成后锁定，前后端均拒绝换绑。

| CLI | 实际模型 | 验证结果 |
| --- | --- | --- |
| Claude Code | Hy4 | 远程平台首轮、云端及远程工作区原生写读、取消后同会话恢复与具体审批通过 |
| Codex | Hy4 | 远程平台首轮、原生 apply_patch、技能脚本与具体审批通过 |
| Pi | Hy4 | 远程平台首轮、原生写读、技能脚本、同轮平台→工作区→平台、浏览器实际审批按钮通过 |
| Kimi | Hy4 | 远程平台首轮、工作区原生写读与具体审批通过 |
| OpenCode | Hy4 | 远程平台调用打开工作区、原生写读与审批、切回平台查询的完整往返通过 |
| Cursor | Auto | 远程平台调用打开工作区、原生写读与审批、切回平台查询的完整往返通过 |
| Dsh | Hy4 | 远程平台首轮、工作区原生写读与具体审批通过 |
| Antigravity | DeepSeek 官方 | 远程平台调用打开工作区、原生写读与审批、切回平台查询的完整往返通过 |
| ZCode | Hy4、DeepSeek 官方 | 官方源码构建 CLI，远程平台首轮、工作区原生写读、技能脚本、拒绝审批、取消后原会话恢复、切换模型与返回平台查询通过；Hy4 的复杂步骤耗时较长 |
| CodeBuddy | Hy4 | 适配与调用限制有自动化覆盖；两份验收凭据均返回 401，未完成真实模型验收 |

网关兼容各 CLI 的 MCP 名称和包装调用，工作区旧平台工具仍在实际调用入口拒绝。模式轮换、旧令牌撤销、审批前无效果、技能可执行位和远程资产同步有自动化回归。浏览器实测了机器选择与锁定、模式切换、具体原生写入审批；点击批准前检查目标不存在，批准后从工作区核对内容。

前端 53 个测试文件、352 项测试及生产构建通过。Linux runtime、httpapi、repo、engine 与其余包回归通过，本轮权限相关 race 测试 ×2 通过。Runner 全量回归中两个注册等待测试在 5 秒等待窗口内超时，针对同一用例单独复测通过；其中基线相同的注册测试也复现超时。新增会话控制与工作区 Runner 用例 ×2 通过，未将全量回归记为无失败。

ZCode 临时凭证由 SessionStart、CLI 退出清理和会话进程结束清理共同保护；即使取消时退出钩子未运行，也在发布进程结束前移除本次凭证，保留原生日志。专项回归及 race ×3 通过。最后一轮实际部署前独立 Review 未发现确认的 P0–P2；Linux runtime 全量回归通过。真实 CLI 验证了拒绝写入无文件效果、取消运行中的 Bash 后凭证文件清零、原 session 写读续聊与切回平台；旧 Hy4 会话改用 DeepSeek 后继续调用平台工具，原生 session ID 保持不变。

第 8 轮修复了收尾延迟复查发现的独立进程组遗漏：ZCode 原生 Bash 可创建新 session，旧的单进程组终止无法阻止其延迟写入。独立 Review 通过后，Linux runtime 全量、专项 ×3 和相关 race ×2 通过；真实 ZCode/DeepSeek 命令开始后取消，跨过延迟写入时点确认无文件，临时凭证清零，原 session ID 不变、写读恢复和返回平台查询通过。补测 Codex 原生 apply_patch 与 Pi 原生写读、具体审批及主机文件内容均通过。独立回归同时验证 double-fork、正常退出、迟到启动和并行会话互不影响。

本轮审批与界面修复经独立 Review 后，在隔离 MySQL、双 API 和远程 Runner 实际部署。已手动移除本地旧审批表，新链路不依赖该表。ZCode/DeepSeek 的原生 Write 等待 75 秒后，由另一个 API 实例批准同一次操作，文件内容精确匹配；拒绝另一项写入后文件不存在。Kimi/Hy4 的受控 loader 在真实原生 Write 中等待、跨 API 批准和完成清理通过。ZCode 另一项写入等待 75 秒后，从另一 API 实例取消生成，确认文件不存在且待确认项清空。

浏览器验证了新会话云端与远程机器反复选择，不创建空会话或改变会话 URL；已开始会话机器只读，后端换绑返回 409。ZCode 待确认卡刷新后保持，位于当前回复末尾且属于消息滚动容器；滚动后卡片位置移动，输入框位置不变。生产构建、前端 53 文件 352 项测试、Linux runtime 全包与本轮 repo/httpapi 审批相关 race 通过。ZCode 首次复用旧工作区时仍有收尾占用，明确拒绝启动；释放后同原生会话重试通过。

本轮 repo 全包通过；httpapi 全包首次受到本机 Git 的 safe.bareRepository=explicit 配置及旧 SSE 用例临时目录清理波动影响，随后仅为测试进程提供 Git 配置，完整复测通过，未修改用户全局 Git 配置。
