# 04 SOP运行与节点执行（v1）

> 文档编号：04
> 文档版本：v1
> 项目：LoopForge
> 关联接口：`GET /api/runs/:id/stream`、`POST /api/runs/:id/pause`、`POST /api/runs/:id/resume`、`POST /api/runs/:id/cancel`
> 状态：已实现
> 证据基线：`develop` @ `6833a9fb`

## 1. 流程目标

引擎领取已启动的 SOP 运行，按 DAG 依赖把就绪节点并行执行；节点在沙箱或执行机器中跑模型 CLI，事件与产物实时入库；全部节点结束、暂停或取消后结算运行。

## 2. 参与角色与前置条件

| 角色 | 职责 |
| --- | --- |
| 成员 | 启动/暂停/恢复/取消运行，订阅进度 |
| SOP 引擎 | 领取运行、续租、调度就绪节点、结算状态 |
| 沙箱 / 执行机器 | 实际执行节点命令与模型 CLI |
| MySQL | runs、run_nodes、run_events、run_artifacts、租约 |

前置条件：运行已由派发或手动启动写入 `runs`；存在可执行的引擎实例（`role=worker/all`）；工作区与仓库准备完成。

## 3. 主流程图

```mermaid
flowchart TD
    A["运行已创建（queued/pending）"] --> C["引擎领取循环（2s）"]
    C --> L{"获得运行租约"}
    L -- "否" --> C
    L -- "是" --> H["心跳续租（TTL/3）并监听控制"]
    H --> R["计算 DAG 就绪节点"]
    R --> P["并行执行就绪节点"]
    P --> N{"节点结果"}
    N -- "成功" --> U["解锁下游节点"]
    N -- "失败" --> F["按失败策略：终止/重试/返工"]
    N -- "等待人工" --> W["运行 waiting / 节点 waiting_approval"]
    U --> R
    F --> S{"全部节点已结算"}
    W --> S
    S -- "否" --> R
    S -- "是" --> T["结算 succeeded / failed"]
    H -->|"控制请求：pause/cancel"| X["暂停或取消并停止在跑节点"]
```

## 4. 时序图

```mermaid
sequenceDiagram
    autonumber
    participant E as SOP 引擎
    participant DB as MySQL
    participant S as 沙箱/执行机器
    participant CLI as 模型 CLI
    participant API as HTTP API
    E->>DB: ResumeInterruptedRuns / 领取运行
    DB-->>E: 运行与节点快照
    loop 执行期
        E->>DB: 续租（lease TTL/3）
        E->>DB: 读取节点状态，计算就绪节点
        E->>S: 执行节点（准备环境+提示词）
        S->>CLI: 调用模型 CLI
        CLI-->>S: 输出与产物
        S-->>E: 事件/日志/产物
        E->>DB: 写入节点与事件
        API-->>API: 推送运行流给订阅者
    end
    E->>DB: 结算运行状态并释放租约
```

## 5. 状态机

```mermaid
stateDiagram-v2
    [*] --> Pending: 创建运行
    Pending --> Running: 引擎领取
    Running --> Waiting: 到达人工关口
    Waiting --> Running: 人工放行
    Running --> Succeeded: 全部节点成功
    Running --> Failed: 节点失败且不可恢复
    Running --> Cancelled: 用户取消
    Waiting --> Cancelled: 用户取消
    Succeeded --> [*]
    Failed --> [*]
    Cancelled --> [*]
```

补充：节点级状态包含 `waiting_approval`（人工关口）与 `pending_input`（等待补充信息）；`paused` 是 Bot 澄清会话的状态，不是运行状态。

## 6. 关键异常与分支

| 场景 | 当前处理 |
| --- | --- |
| 实例重启 | `ResumeInterruptedRuns` 周期性恢复中断运行 |
| 租约丢失 | 心跳续租失败即取消本地执行，避免双写 |
| 用户暂停 | `PauseRequested` 停止在跑节点后写入暂停态 |
| 用户取消 | `CancelRequested` 停止节点并结算 cancelled |
| 节点等待人工 | 运行置 waiting，节点 waiting_approval，等待审批 |
| 分支失败 | 按节点失败策略终止、重试或返工，不让同批节点继续写终态 |
| 红线：状态终态后被写 | `stopNodes` 先取消并等待节点退出，再写终态 |

## 7. 证据索引

| 证据 | 位置 |
| --- | --- |
| 启动运行 | `internal/engine/run_start.go` |
| 领取循环 | `internal/engine/run_state.go`（`StartClaimLoop`，2s） |
| 执行主循环 | `internal/engine/node_execute.go`（`execute`） |
| 节点执行 | `internal/engine/node_execute.go`（`executeNode`） |
| 并行分支 | `internal/engine/parallel_exec.go` |
| SOP 节点 | `internal/engine/sop_node.go` |
| 运行控制 | `internal/engine/run_control.go`、`internal/httpapi/run_control.go` |
| 运行状态 | `internal/domain/run.go` |

## 8. 实现风险与待确认

- 运行状态使用字符串字面量，无集中枚举；新增状态需同步引擎、界面与统计。
- 并行分支不受波次限制，节点耗时不均时排障需要看节点级事件而不是运行总时长。
- 中断恢复依赖租约与实例心跳，多实例部署需保证时钟与租约 TTL 一致。
