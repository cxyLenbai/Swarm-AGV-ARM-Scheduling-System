# Backend 后端功能清单（推荐实现顺序）

> 目标：先把“可运行骨架 + 可观测 + 安全边界”打牢，再逐步补齐“设备→任务→调度→实时→事件流→AI对接”。

## 0. 约定：目录职责（你现在的结构）

- `backend/go/api/`：业务 API 网关层（Gin），对外提供 REST/WebSocket，聚合内部服务
- `backend/go/core/`：核心调度/实时处理层（对接 OpenRMF/路径规划/设备通信等性能关键逻辑）
- `backend/go/shared/`：Go 公共库（配置、日志、鉴权中间件、错误码、工具等）
- `backend/python/services/`：Python 服务层（FastAPI），提供“算法能力的服务化入口”（不是算法本体）
- `backend/python/shared/`：Python 公共库（配置、日志、DTO、客户端等）
- `backend/proto/`：gRPC/事件契约（proto / schema），用于 Go↔Python 或服务间契约
- `backend/configs/`：配置模板（dev/staging/prod）
- `backend/deployments/`：部署清单（k8s、helm 等）
- `backend/scripts/`：本地开发/运维脚本

---

## 1. 基础工程骨架（先跑起来）

**产出**
- Go：`api` 与 `core` 两个可启动服务（最小 HTTP 端口 + healthz）
- Python：`services` 一个可启动服务（healthz）

**建议落点**
- `backend/go/api/cmd/api/`、`backend/go/core/cmd/core/`
- `backend/python/services/app/`

---

## 2. 配置管理、日志、错误码（全局能力）

**产出**
- 配置加载：支持环境变量 + 配置文件（dev/prod 分离）
- 结构化日志：统一字段（`trace_id`、`robot_id`、`task_id`、`tenant_id`）
- 统一错误响应：错误码 + message + 可观测字段

**建议落点**
- `backend/go/shared/config/`、`backend/go/shared/log/`、`backend/go/shared/errors/`
- `backend/python/shared/config/`、`backend/python/shared/logging/`

---

## 3. 健康检查、优雅退出、基础中间件

**产出**
- `GET /healthz`、`GET /readyz`
- 超时、限流、CORS、请求日志、RequestID
- 优雅退出（SIGTERM）、连接池关闭

**建议落点**
- `backend/go/shared/httpmw/`（Gin middleware）

---

## 4. 认证与授权（先把门装上）

**产出**
- OIDC/Keycloak 接入（JWT 验签、JWKS 缓存轮转）
- RBAC/多租户：realm/tenant/role 的鉴权中间件
- 审计日志（谁在何时对什么资源做了什么）

**建议落点**
- `backend/go/api/internal/auth/`
- `backend/go/shared/auth/`

---

## 5. 数据库与数据模型（先建“事实来源”）

**产出（最小可用）**
- PostgreSQL 连接池、迁移（migrations）
- 基础表：`tenants`、`users`、`robots`、`maps`、`tasks`、`task_events`
- Repository 层（可替换 ORM/SQLC）

**建议落点**
- `backend/go/api/internal/db/`、`backend/go/api/internal/repo/`
- 数据库迁移文件放在 `backend/database/` 或 `backend/go/api/migrations/`（二选一，后续统一即可）

---

## 6. 设备管理与状态接入

**产出**
- 设备注册/更新能力（能力集、型号、协议、区域）
- 心跳/在线状态、位置、电量等状态上报入口
- 状态写入时序库（Influx/Timescale 二选一）或先写 Postgres（MVP）

**建议落点**
- `backend/go/api/internal/devices/`
- `backend/go/core/internal/comm/`（协议/设备通信适配）

---

## 7. 地图/拓扑/区域与路径资源管理

**产出**
- 地图上传/版本管理（仓库布局、区域、禁行区）
- 拓扑图（节点/边/权重）与校验

**建议落点**
- `backend/go/api/internal/maps/`
- `backend/go/core/internal/path/`（路径资源/路网）

---

## 8. 任务模型与工作流

**产出**
- 任务创建/取消/查询
- 任务状态机（Created→Assigned→Executing→Succeeded/Failed/Cancelled）
- 幂等：任务创建幂等键、状态流转幂等

**建议落点**
- `backend/go/api/internal/tasks/`
- `backend/go/core/internal/workflow/`

---

## 9. 调度编排

**产出**
- 调度决策接口：输入（任务、地图、机器人状态）→ 输出（分配/路径/时序）
- 与 OpenRMF 的集成边界（适配层/客户端/事件映射）
- 冲突检测、重规划触发条件

**建议落点**
- `backend/go/core/internal/scheduler/`
- `backend/go/core/internal/rmf/`（OpenRMF 适配）

---

## 10. 实时推送（前端看得见）

**产出**
- WebSocket/SSE：推送机器人状态、任务进度、告警
- 订阅粒度：按 tenant / warehouse / robot_id

**建议落点**
- `backend/go/api/internal/realtime/`

---

## 11. 事件流与消息队列（Kafka）

**产出**
- Topic 规划：`robot.status`、`task.events`、`alerts`、`scheduler.decisions`
- 消费者：状态入库、告警触发、AI 特征流
- Outbox/Inbox（可选）：保证 DB 与事件一致性

**建议落点**
- `backend/go/shared/mq/`
- `backend/go/core/internal/stream/`

---

## 12. 缓存与分布式锁（Redis）

**产出**
- 热点数据缓存（机器人最新状态、任务视图）
- 分布式锁（任务分配/重规划避免并发冲突）
- 限流计数器

**建议落点**
- `backend/go/shared/cache/`

---

## 13. 异步任务（Asynq）

**产出**
- 延迟任务：超时补偿、定时对账、周期性清理
- 重试/死信队列

**建议落点**
- `backend/go/api/internal/jobs/` 或 `backend/go/core/internal/jobs/`（按职责放置）

---

## 14. Python 服务对接（把算法“服务化”，算法仍在 `algorithms/`）

**产出**
- Go→Python：HTTP/gRPC 客户端 + 超时/重试/熔断
- 契约（proto/schema）：输入输出稳定，便于演进
- Python 侧：仅提供推理/评估 API，算法实现继续放在 `algorithms/`

**建议落点**
- `backend/go/shared/clients/ai/`
- `backend/python/services/`（FastAPI 路由）
- `backend/proto/`

---

## 15. 可观测性（生产必备）

**产出**
- Metrics：Prometheus 指标（QPS、延迟、分配耗时、冲突次数、Kafka lag）
- Tracing：OpenTelemetry（HTTP/gRPC/Kafka）
- 日志关联：trace_id 串起来

**建议落点**
- `backend/go/shared/observability/`
- `backend/python/shared/observability/`

---

## 16. 部署与运维（最后固化）

**产出**
- Dockerfile（Go/Python 分开）
- K8s manifests（Deployment/Service/HPA/ConfigMap/Secret）
- 运行手册（本地启动、环境变量、依赖服务）

**建议落点**
- `backend/deployments/k8s/`
- `backend/docs/`

---

## 17. 测试与压测（持续补齐）

**产出**
- 单元测试：状态机、鉴权、序列化
- 集成测试：DB/Redis/Kafka（可用 docker-compose 或 testcontainers）
- 压测：核心 API、WebSocket 连接、调度决策耗时

**建议落点**
- `backend/go/api/internal/**/`（就近放测试）
- `backend/scripts/`（压测脚本）

---

## 18. 双周迭代排期（单人｜5个月上线｜从12月底开始｜上线必须包含 Kafka/Outbox、Redis/分布式锁、Asynq、InfluxDB、OpenRMF 深度集成、Python 算法服务对接）

### 18.1 上线必须具备（Hard Requirements）
- **事件流**：Kafka Topic 规划 + Producer/Consumer + **Outbox**（保证 DB 与事件一致性）
- **缓存与锁**：Redis 缓存（热点视图）+ **分布式锁**（任务分配/重规划并发控制）
- **异步任务**：Asynq（延迟/重试/死信），用于 Outbox 投递、补偿、周期任务
- **时序**：InfluxDB（状态/定位/电量等时序写入与查询，含保留策略）
- **OpenRMF 深度集成**：真实对接（不是 stub），事件映射与双向状态同步跑通
- **Python 算法服务**：Go→Python（HTTP/gRPC）真实调用 + 超时/重试/熔断 + 契约（proto/schema）

### 18.2 建议架构约定（降低单人交付风险）
- **Postgres 做“事实来源”**：业务实体（robots/tasks/task_events/outbox_events）都以 Postgres 为准
- **InfluxDB 做“高频时序”**：机器人状态全量写入 Influx；Postgres 只保留“最新快照/关键节点”
- **Outbox→Kafka 用 Asynq Worker 投递**：失败重试/死信可控；消费者侧一律做幂等（按 event_id 去重）
- **OpenRMF/算法对接都先定契约**：先协议稳定、再性能优化（单人更容易控复杂度）

### 18.3 Sprint 排期（10 个双周 / 约 20 周）

#### Sprint 1（12/30–01/12）：三服务骨架 + 全局规范 + 本地可跑
- Go `api/core`、Python `services` 可启动；`GET /healthz`、`GET /readyz`
- 配置（env+file）骨架；结构化日志字段定版；统一错误码/错误响应
- 基础中间件：RequestID、请求日志、超时（先最小版）

#### Sprint 2（01/13–01/26）：鉴权/多租户 + Postgres 事实来源
- OIDC/JWT 验签（JWKS 缓存轮转）+ tenant 隔离中间件 + 审计日志最小版
- Postgres 连接池 + migrations；核心表：`tenants/users/robots/tasks/task_events`
- Repo 层最小实现（支撑后续设备/任务）

#### Sprint 3（01/27–02/09）：设备接入 MVP（先让“车数据”进来）
- 设备注册/更新 API；状态上报（心跳/在线/位置/电量）
- 状态入库（先 Postgres 做 MVP）+ 查询“最新状态”API
- 基础中间件补齐：CORS、限流（先简单策略）

#### Sprint 4（02/10–02/23）：任务模型 + 状态机闭环（DB 侧打通）
- 任务创建/取消/查询；创建幂等键；状态流转幂等
- core `workflow` 状态机落地；`task_events` 记录关键流转
- 为事件流做准备：定义 task/robot 领域事件的字段集合（event_id/tenant_id/occurred_at 等）

#### Sprint 5（02/24–03/09）：Kafka 基座 + Topic/Schema 定版 + 最小生产消费链路
- Topic 定版：`robot.status`、`task.events`、`alerts`、`scheduler.decisions`
- Go shared：Kafka Producer/Consumer 封装（含重试、consumer group、可观测字段）
- 最小消费者：`task.events` 入库（或落审计表），并验证消费幂等策略

#### Sprint 6（03/10–03/23）：Outbox + Asynq（保证一致性与可恢复）
- migrations：增加 `outbox_events`（建议字段：event_id、aggregate_type、aggregate_id、topic、payload、status、attempts、next_retry_at）
- 写入与业务事务绑定：任务创建/状态流转/调度决策均写 outbox
- Asynq worker：扫描/锁定 outbox → 投递 Kafka → 标记完成；失败重试/死信

#### Sprint 7（03/24–04/06）：Redis 缓存 + 分布式锁（调度并发控制）
- Redis 缓存：机器人最新状态视图、任务视图（读优化）
- 分布式锁：任务分配/重规划互斥；锁粒度与超时策略定版
- 为实时推送做准备：状态/任务视图的订阅维度（tenant/warehouse/robot_id）

#### Sprint 8（04/07–04/20）：InfluxDB 落地（写入/保留/查询）+ 状态管线迁移
- InfluxDB：bucket/retention policy、measurement/tags/fields 设计定版（注意 tag 基数）
- 写入管线：机器人高频状态写入 Influx（line protocol/SDK）+ Postgres 保留最新快照
- 查询 API：按 `robot_id` + time range 查询轨迹/电量等时序（含分页/下采样策略）

#### Sprint 9（04/21–05/04）：OpenRMF 深度集成（真实对接跑通）
- core `rmf` 适配层实现：任务下发/取消、状态回传、事件映射（与内部 task/workflow 对齐）
- 调度与 RMF 闭环：冲突检测、重规划触发条件（先做关键触发与可观测）
- 验收：在 RMF 环境（仿真或测试场）跑通端到端任务执行与状态同步

#### Sprint 10（05/05–05/18）：Python 算法服务对接 + 上线收口（可部署、可观测、可回滚）
- `backend/proto/` 契约定版；Go→Python gRPC/HTTP（含超时/重试/熔断）；feature flag 控制启用
- 实时推送：WS/SSE 推送机器人状态、任务进度、告警（按 tenant/robot_id 订阅）
- 观测收口：Prometheus 指标（HTTP、Kafka lag、Asynq 队列堆积、Influx 写入失败、调度耗时）+ Tracing（HTTP/gRPC/Kafka）
- 部署收口：Dockerfile + k8s manifests（Deployment/Service/HPA/Config/Secret）+ 运行手册
- 测试收口：状态机/鉴权单测；DB/Kafka/Redis/Asynq/Influx 最小集成测试（可用 docker-compose 或 testcontainers）

#### 缓冲（05/19–05/31）：联调、压测、故障演练与参数调优
- 故障演练：Kafka/Redis/Influx/DB 断连恢复，验证 outbox+重试+幂等
- 压测：核心 API、WS 连接、调度决策耗时；针对瓶颈做参数调优与限流策略校准
