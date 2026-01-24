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
- Go：`api` 与 `core` 两个可启动服务（最小 HTTP 端口 + `GET /healthz` + `GET /readyz`）
- Python：`services` 一个可启动服务（`GET /healthz` + `GET /readyz`）

**建议落点**
- `backend/go/api/cmd/api/`、`backend/go/core/cmd/core/`
- `backend/python/services/app/`

**TODO（按优先级）**
- 统一启动方式：提供本地启动脚本/命令（例如 `make dev-api`、`make dev-core`、`make dev-py`），并打印启动信息（服务名/环境/端口/版本）
- 最小路由：3 个服务都提供 `GET /healthz`（进程存活即可 200）
- 最小就绪检查：3 个服务都提供 `GET /readyz`（配置加载成功 + 关键依赖就绪才 200；依赖未接入前先做“配置校验通过”）
- 生命周期骨架：启动时完成 config/logger 初始化；退出时优雅停止（预留连接池/consumer 关闭钩子）

**验收**
- 3 个服务都能本地启动并响应 `GET /healthz`、`GET /readyz`
- `GET /readyz` 在缺少关键配置时返回非 200（建议 503）并输出统一错误响应

---

## 2. 配置管理、日志、错误码（全局能力）

**产出**
- 配置加载：支持环境变量 + 配置文件（dev/prod 分离），并具备校验与默认值策略
- 结构化日志：统一字段与事件命名，Go/Python 输出格式一致（至少 JSON）
- 统一错误响应：错误码 + message + request_id + details，并覆盖 4xx/5xx/校验错误

**建议落点**
- `backend/go/shared/config/`、`backend/go/shared/log/`、`backend/go/shared/errors/`
- `backend/python/shared/config/`、`backend/python/shared/logging/`

**配置 TODO**
- 配置来源与优先级定版：`config file < env`（可加 `ENV=dev/staging/prod` 切换默认配置文件）
- 配置模型类型化（Go struct / Python Settings），对必填项做校验；敏感字段禁止打印明文
- 提供模板：`backend/configs/` 下给出 `dev.example.*`、`prod.example.*`（不提交密钥）

**日志 TODO（字段定版：最小集合）**
- 基础字段：`ts`、`level`、`service`、`env`、`version`（可选）、`event`、`msg`
- HTTP 字段：`request_id`、`method`、`path`、`status_code`、`duration_ms`、`client_ip`（可选）
- 业务字段（按需出现在相关日志中）：`trace_id`（预留）、`tenant_id`、`robot_id`、`task_id`
- 错误字段：`error_code`、`error`（简要信息）、`stack`（生产环境可裁剪）

**错误码/错误响应 TODO**
- 错误码分段与命名规范定版（示例：`INVALID_ARGUMENT`、`UNAUTHENTICATED`、`FORBIDDEN`、`NOT_FOUND`、`CONFLICT`、`INTERNAL_ERROR`、`TIMEOUT`）
- 统一错误响应体（Go/Python 一致）：
  - `{"error":{"code":"...","message":"...","request_id":"...","details":{...}}}`
- 映射规则：参数校验/路由不存在/业务异常/未知异常都有稳定 `code` 与 HTTP status

**验收**
- 任何错误都返回统一结构（包括 404/422/500）
- 日志与错误响应中的 `request_id` 一致，可用于定位单次请求全链路日志

---

## 3. 健康检查、优雅退出、基础中间件

**产出**
- `GET /healthz`、`GET /readyz`
- 超时（最小版）、请求日志、RequestID（先最小版，限流/CORS 后补齐）
- 优雅退出（SIGTERM）、连接池/consumer 关闭（预留）

**建议落点**
- `backend/go/shared/httpmw/`（Gin middleware）

**中间件 TODO（先最小可用）**
- RequestID：读取/生成 `X-Request-ID`，写回响应头；注入日志上下文
- 请求日志：记录开始/结束、`duration_ms`、`status_code`；可对 `/healthz` 降噪
- 超时：对 handler 设置上限（Go `context.WithTimeout` / Python `anyio.fail_after`），超时返回 504 + 统一错误响应（`code=TIMEOUT`）

**优雅退出 TODO**
- 捕获 SIGTERM/SIGINT：停止接收新请求，等待 in-flight 完成（带最大等待时间），然后关闭资源

**验收**
- 每个响应都带 `X-Request-ID`
- 超时能稳定返回 504，且符合统一错误响应体，并在日志中带 `event` 与 `error_code`

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

### 18.3 Sprint 排期（11 个双周 / 约 22 周）

#### Sprint 1（12/30–01/12）：三服务骨架 + 全局规范 + 本地可跑
- **服务可启动（本地可跑）**
  - [x] Go `api`：`backend/go/api/cmd/api/` 可启动（HTTP），启动日志打印 `service/env/port/version`
  - [x] Go `core`：`backend/go/core/cmd/core/` 可启动（HTTP），启动日志打印 `service/env/port/version`
  - [x] Python `services`：`backend/python/services/app/` 可启动（FastAPI/HTTP），启动日志打印 `service/env/port/version`
  - [x] 统一本地启动入口：提供 `backend/Makefile` + `backend/scripts/dev.ps1`（例如 `make dev-api|dev-core|dev-py` 或 `powershell -File backend/scripts/dev.ps1 -Target api|core|py`），默认使用 `ENV=dev`
- **健康检查**
  - [x] 3 个服务都提供 `GET /healthz`：进程存活即 200（不做外部依赖探测）
  - [x] 3 个服务都提供 `GET /readyz`：至少完成“配置加载 + 配置校验”才 200；不满足则 503 + 统一错误响应
- [x] **配置（env + file）骨架**
  - 配置来源优先级：`config file < env`；支持 `ENV=dev|staging|prod` 选择默认配置文件（可被 `CONFIG_PATH` 覆盖）
  - 统一最小配置键（3 服务对齐命名）：`ENV`、`SERVICE_NAME`、`HTTP_PORT`、`LOG_LEVEL`、`CONFIG_PATH`、`REQUEST_TIMEOUT_MS`
  - 类型化配置模型 + 默认值策略：缺少必填配置时 `readyz` 不就绪（并返回稳定错误码）；敏感字段禁止明文落日志
  - 配置模板落地：`backend/configs/` 提供 `dev.example.*`（不提交密钥/口令）
- [x] **结构化日志字段定版（Go/Python 对齐，至少 JSON）**
  - 必需字段：`ts`、`level`、`service`、`env`、`event`、`msg`、`request_id`（若是请求内日志）
  - HTTP 访问日志：`method`、`path`、`status_code`、`duration_ms`（`/healthz` 可降噪）
  - 错误字段：`error_code`、`error`（简要）、`stack`（prod 可裁剪/关闭）
- [x] **统一错误码/错误响应（Go/Python 对齐）**
  - 错误码集合（最小可用）：`INVALID_ARGUMENT`、`NOT_FOUND`、`CONFLICT`、`UNAUTHENTICATED`、`FORBIDDEN`、`TIMEOUT`、`INTERNAL_ERROR`、`FAILED_PRECONDITION`
  - 统一错误响应体：`{"error":{"code":"...","message":"...","request_id":"...","details":{...}}}`
  - 必须覆盖：参数校验错误、路由不存在（404）、未知异常（500）、超时（504）
- [x] **基础中间件（先最小可用，后续 Sprint 再补齐 CORS/限流/Tracing）**
  - RequestID：读取/生成 `X-Request-ID`，写回响应头；注入日志上下文；错误响应携带同一个 `request_id`
  - 请求日志：记录开始/结束与关键字段（见上）；可对健康检查降噪
  - 超时：按 `REQUEST_TIMEOUT_MS` 设置请求上限；超时返回 504 + `code=TIMEOUT` + 统一错误响应
- **本 Sprint 验收**
  - 3 个服务均可一条命令本地启动，并通过 `GET /healthz`（200）与 `GET /readyz`（配置正确时 200）
  - 缺少关键配置时：`GET /readyz` 返回 503，且响应体/日志字段满足“统一错误响应 + request_id 可追踪”

#### Sprint 2（01/13–01/26）：鉴权/多租户 + Postgres 事实来源
- **目标**：把“谁能访问/访问哪个租户/访问行为可追溯/数据落到 Postgres”这 4 条安全与事实来源链路跑通，为 Sprint 3/4 的设备与任务闭环铺路。
- [x] **OIDC/JWT 鉴权（JWKS 缓存轮转）**
  - [x] 统一配置键（Go/Python 名称对齐）：`OIDC_ISSUER`、`OIDC_AUDIENCE`、`OIDC_JWKS_URL`（可选，默认 issuer/.well-known/jwks.json）、`JWKS_CACHE_TTL_SECONDS`、`JWT_CLOCK_SKEW_SECONDS`
  - 统一鉴权策略：仅接受 `Authorization: Bearer <jwt>`；校验 `iss/aud/exp/nbf`；允许少量 clock skew；错误映射 `UNAUTHENTICATED`
  - JWKS 缓存与轮转：按 `kid` 索引 key；命中失败时触发一次刷新；TTL 到期后台刷新（或下一次请求刷新）；网络失败时允许使用未过期缓存
  - 产出：通用“鉴权结果上下文”结构（`subject/email/name/roles/claims` 等）+ 中间件（Go `api` 优先，Python 服务如需对外暴露也复用）
- [x] **Tenant 隔离中间件（强制租户边界）**
  - 租户识别策略定版（建议最小可用）：请求头 `X-Tenant-ID`（或 `X-Tenant-Slug`）为主；JWT claims 中可选携带 `tenant_id/tenants` 做二次校验（header 与 token 不一致则 `FORBIDDEN`）
  - 在请求上下文中注入 `tenant_id`，并要求所有 Repo 查询都必须带 `tenant_id` 过滤（禁止“无租户条件”的全表操作）
  - 日志字段补齐：请求日志/错误日志统一带 `tenant_id`（如果解析到）
  - 最小验证端点：`GET /api/v1/me`（返回用户 claims）+ `GET /api/v1/tenants/current`（返回当前 tenant）
- [x] **审计日志最小版（可追溯）**
  - 审计事件范围（先最小可用）：登录/鉴权失败、写接口（POST/PUT/PATCH/DELETE）、关键资源（robots/tasks）读写
  - 记录字段（建议）：`audit_id`、`occurred_at`、`tenant_id`、`actor_user_id`（可空）/`subject`、`action`、`resource_type/resource_id`（可空）、`request_id`、`method/path/status_code/duration_ms`、`client_ip/user_agent`、`details(jsonb)`
  - 写入策略：请求结束后异步写入（失败只告警不影响主请求）；提供最小开关（例如 `AUDIT_ENABLED`）
- [x] **Postgres 连接池（服务可读写事实来源）**
  - 统一配置键：`DATABASE_URL`、`DB_MAX_CONNS`、`DB_MIN_CONNS`、`DB_CONN_MAX_IDLE_SECONDS`、`DB_CONN_MAX_LIFETIME_SECONDS`
  - 连接池封装（建议 `pgxpool`）：初始化/关闭钩子；`readyz` 增加 `SELECT 1` 探测（失败则 503）
  - 迁移执行策略：本地/CI 启动前跑 migrations（或服务启动时可选自动迁移，默认关闭）
- [x] **Migrations（事实来源 Schema 定版）**
  - 落点建议：`backend/migrations/`（按时间戳排序），并提供一条命令执行（`make migrate-up` / `backend/scripts/migrate.*`）
  - 核心表（最小可用）
    - `tenants`：`tenant_id`（uuid pk）、`slug`（unique）、`name`、`created_at`
    - `users`：`user_id`（uuid pk）、`tenant_id`（fk）、`subject`（oidc sub）、`email`、`display_name`、`role`、`created_at`、`last_login_at`、唯一约束（`tenant_id, subject`）
    - `robots`：`robot_id`（uuid pk）、`tenant_id`（fk）、`robot_code`（tenant 内 unique）、`display_name`、`status`（可选）、`updated_at`
    - `tasks`：`task_id`（uuid pk）、`tenant_id`（fk）、`task_type`、`status`、`idempotency_key`（tenant 内 unique）、`payload`（jsonb）、`created_by_user_id`（可空）、`created_at`、`updated_at`
    - `task_events`：`event_id`（uuid pk）、`tenant_id`（fk）、`task_id`（fk）、`event_type`、`from_status/to_status`（可空）、`occurred_at`、`actor_user_id`（可空）、`payload`（jsonb）；索引（`tenant_id, task_id, occurred_at`）
    - `audit_logs`（若采纳审计表）：按上文字段定版并加索引（`tenant_id, occurred_at`、`request_id`）
  - 约束要求：所有表必须有 `tenant_id`；FK on delete 策略定版（建议先 RESTRICT/NO ACTION，避免误删扩散）
- [x] **Repo 层最小实现（支撑后续设备/任务）**
  - 仓储接口（按领域拆分）：`TenantsRepo/UsersRepo/RobotsRepo/TasksRepo/AuditRepo`（都以 `tenant_id` 为必传参数）
  - 关键方法（最小集合）
    - tenants：`CreateTenant`、`GetTenantByID/Slug`
    - users：`UpsertUserFromOIDC`（`tenant_id + subject`）、`GetUserByID`
    - robots：`UpsertRobot`、`GetRobotByCode`、`ListRobots`
    - tasks：`CreateTask`（支持 `idempotency_key` 幂等返回）、`GetTaskByID`、`ListTasks`、`AppendTaskEvent`
    - audit：`WriteAuditLog`（可批量）
  - 事务边界：`CreateTask + AppendTaskEvent` 需同事务；并发幂等（唯一键冲突返回已有记录）
- **待定决策 / 风险点（尽早拍板）**
  - 租户定位：header（简单）vs 子域名/路径（更接近 SaaS）；建议先 header，后续可扩展
  - token→角色映射：直接使用 `roles/scopes` claim 还是做 DB 侧用户角色（建议先 token 直出 + 预留 DB 覆盖）
  - JWKS 可用性：外部网络抖动会导致验签失败；需要明确超时、重试与“仅使用未过期缓存”的降级策略
  - 本地开发：提供 dev issuer/JWKS（或 mock 模式），避免每个开发者都依赖外部 IdP
- **本 Sprint 验收**
  - 非公开路由默认必须鉴权：无 token/无效 token 返回 401 + `UNAUTHENTICATED`（统一错误响应）
  - 多租户隔离：跨 tenant 访问同一 `robot_code/task_id` 必须 404 或 403（策略明确且一致）；任何 Repo 都不允许“无 tenant 条件”查询
  - `readyz` 同时校验配置 + DB 可用；migrations 可重复执行且不会破坏已有数据（幂等/版本控制）
  - 审计日志（若开启）在写接口请求后可在 DB 查到对应记录，并带 `request_id/tenant_id/subject/status_code`

#### Sprint 3（01/27–02/09）：设备接入 MVP（先让“数据”进来）
- [x] 设备注册/更新 API；状态上报（心跳/在线/位置/电量）
- [x] 状态入库（先 Postgres 做 MVP）+ 查询“最新状态”API
- [x] 基础中间件补齐：CORS、限流（先简单策略）

#### Sprint 4（02/10–02/23）：任务模型 + 状态机闭环（DB 侧打通）
- [x] 任务创建/取消/查询；创建幂等键；状态流转幂等
- [x] core `workflow` 状态机落地；`task_events` 记录关键流转
- [x] 为事件流做准备：定义 task/robot 领域事件的字段集合（event_id/tenant_id/occurred_at 等）

#### Sprint 5（02/24–03/09）：Kafka 基座 + Topic/Schema 定版 + 最小生产消费链路
- [x] Topic 定版：`robot.status`、`task.events`、`alerts`、`scheduler.decisions`、`congestion.metrics`、`congestion.predictions`
- [x] Go shared：Kafka Producer/Consumer 封装（含重试、consumer group、可观测字段）
- [x] 最小消费者：`task.events` 入库（或落审计表），并验证消费幂等策略

#### Sprint 6（03/10–03/23）：Outbox + Asynq（保证一致性与可恢复）
- [x] migrations：增加 `outbox_events`（建议字段：event_id、aggregate_type、aggregate_id、topic、payload、status、attempts、next_retry_at）
- [x] 写入与业务事务绑定：任务创建/状态流转/调度决策均写 outbox
- [x] Asynq worker：扫描/锁定 outbox → 投递 Kafka → 标记完成；失败重试/死信

#### Sprint 7（03/24–04/06）：Redis 缓存 + 分布式锁（调度并发控制）
- [x] Redis 缓存：机器人最新状态视图、任务视图（读优化）
- [x] 分布式锁：任务分配/重规划互斥；锁粒度与超时策略定版
- [x] 为实时推送做准备：状态/任务视图的订阅维度（tenant/warehouse/robot_id）

#### Sprint 8（04/07–04/20）：InfluxDB 落地（写入/保留/查询）+ 状态管线迁移
- [x] InfluxDB：bucket/retention policy、measurement/tags/fields 设计定版（注意 tag 基数）
- [x] 写入管线：机器人高频状态写入 Influx（line protocol/SDK）+ Postgres 保留最新快照
- [x] 扩展时序模型：落地 `ZoneCongestionTimeSeries`（区域/路段拥堵指数、均速、排队长度、risk/confidence）
- [x] 查询 API：按 `robot_id` + time range 查询轨迹/电量等时序；按 `zone_id` + time range 查询拥堵时序（含分页/下采样策略）

#### Sprint 9（04/21–05/04）：仓内拥堵热点（MVP）+ 预警闭环 + 调度联动
- [x] 数据模型：区域/路段实体（PostGIS）+ 拥堵预警事件表 + 人工处置/回滚记录（为“预测-预警-处置-复盘”闭环留口）
- [x] 特征与指数：从 `robot.status`/`task.events` 做窗口聚合，产出 `congestion_index`/`risk_score`/`confidence`（先 Go worker/消费者实现，后续可替换 Flink）
- [x] 预警引擎：L1/L2/L3 阈值 + 迟滞/冷却时间，输出到 `alerts`（或 `congestion.*`）并记录入库
- [x] API/实时：`GET /api/v1/congestion/hotspots?horizon_seconds=...`（先支持 0/短窗）+ WS 推送 `congestion_alert`/`congestion_heatmap_update`
- [x] 调度联动：根据拥堵指数动态调整路段成本/限流参数，触发重规划（先最小可用 + 可观测）

#### Sprint 10（05/05–05/18）：OpenRMF 深度集成（真实对接跑通）
- [x] core `rmf` 适配层实现：任务下发/取消、状态回传、事件映射（与内部 task/workflow 对齐）
- [x] 调度与 RMF 闭环：冲突检测、重规划触发条件（先做关键触发与可观测）
- [x] 验收：在 RMF 环境（仿真或测试场）跑通端到端任务执行与状态同步

#### Sprint 11（05/19–06/01）：Python 算法服务对接 + 拥堵预测（1/5/15min）+ 上线收口
- [x] `backend/proto/` 契约定版：Go→Python gRPC/HTTP（含超时/重试/熔断）；新增 `PredictCongestionHotspots`/`suggested_actions` 等字段；feature flag 控制启用/回退
- [x] 在线推理服务：FastAPI（可选 Ray Serve）读取 Redis/Influx/PostGIS 特征，输出热点热力图与预警建议（1/5/15min 滚动预测）
- [x] 实时推送：WS/SSE 推送机器人状态、任务进度、告警（含拥堵预警）（按 tenant/robot_id/warehouse 订阅）
- [x] 观测收口：Prometheus 指标（HTTP、Kafka lag、Asynq 堆积、Influx 写入失败、调度耗时、推理延迟/吞吐/命中率）+ Tracing（HTTP/gRPC/Kafka）
- [x] 部署收口：Dockerfile + k8s manifests（Deployment/Service/HPA/Config/Secret）+ 运行手册
- [x] 测试收口：状态机/鉴权单测；DB/Kafka/Redis/Asynq/Influx 最小集成测试（可用 docker-compose 或 testcontainers）

#### 缓冲（06/02–06/14）：联调、压测、故障演练与参数调优
- 故障演练：Kafka/Redis/Influx/DB 断连恢复，验证 outbox+重试+幂等
- 压测：核心 API、WS 连接、调度决策耗时；针对瓶颈做参数调优与限流策略校准
- 效果调优：拥堵预警阈值/迟滞/冷却时间校准；误报/漏报复盘与回放验证

---

## 19. 10 万级规模扩展路线图（新增）

> 目标：在不破坏现有功能的前提下，完成“多集群分区 + 分层调度 + 流式计算 + 边缘网关 + 数字孪生仿真”能力，支撑 10 万级 AGV 规模与高频状态流。

### 19.1 规模目标（需业务确认的硬指标）
- 规模基线：AGV 10 万；在线率 ≥ 95%；状态频率 ≥ 1Hz（可分层）
- 时延目标：关键控制指令 P99 < 200ms；调度决策 P99 < 1s；全链路可观测
- 地图规模：区域/路段/节点数量级；多仓/多园区
- 峰值与降级：流量激增或局部拥堵时，能按区域/租户隔离与降级

### 19.2 架构演进（从单集群到多集群分区）
- 多集群分区：按仓/区域/租户分片（Cluster A/B/C），主路由按 `tenant_id + warehouse_id`
- 控制面与数据面拆分：全局控制面只存“索引 + 元数据”；数据面承载实时流
- 事件流按 key 分区：`robot_id/zone_id/warehouse_id` 做 topic key；热点隔离与再平衡策略定版
- 跨集群事件桥接：关键事件做双写或桥接（MirrorMaker2 或自研 bridge）
- 数据一致性策略：跨集群只保证最终一致，强一致仅在局部集群内

### 19.3 调度层升级（分层调度 + 增量重规划）
- 分层调度：全局粗分配（跨仓/跨区域）+ 局部细排程（单仓实时）
- 批量决策：对批量任务做合并调度，减少全量重算
- 增量重规划：基于局部变化与拥堵事件触发，避免全图重算
- 冲突检测分区化：按区域/路段分区执行，减少全域锁

### 19.4 时序与状态（流式计算 + 分层存储）
- 流式计算：引入 Flink/Kafka Streams 做窗口聚合与特征生成
- 状态分层：热缓存（Redis）+ 温存储（Postgres 最新快照）+ 冷存储（Influx 历史）
- 采样与降频：按 `robot_id`/`priority` 实施动态降频
- 事件回放能力：支持回放指定时间窗（用于仿真/调参）

### 19.5 通信与控制（边缘网关 + QoS）
- 边缘代理：设备侧网关聚合状态上报/控制指令，下沉认证与限流
- 控制通道 QoS：指令通道优先级（控制 > 任务 > 监控）
- 断连自治：网关具备短时自主策略与离线缓冲
- 轻量协议：MQTT/GRPC bidi 组合，减少中心压力

### 19.6 可观测与容量（SLO + 压测模型）
- 端到端 tracing：覆盖 API→调度→事件流→设备→回执全链路
- 压测模型：Kafka/Redis/Postgres/Influx 组合压测；定义并固化 SLO
- 弹性策略：HPA + 队列深度 + Lag 联动扩缩容
- 故障演练：按“单仓/单集群/跨集群”进行分级演练

### 19.7 数字孪生（仿真引擎 + 实时对齐）
- 引擎选型：RMF sim / Gazebo / Unity / Isaac Sim（二选一或多栈）
- 实时对齐：仿真与真实状态以 `robot_id` 对齐，支持漂移检测与纠偏
- 回放与调参：基于历史事件流回放，支持参数 A/B
- 输出闭环：仿真结果回写特征与预警策略

### 19.8 最小可行验证（MVP）清单
- 1 个多集群分区：按 `warehouse_id` 分片 + 统一路由网关
- 1 条分层调度链路：全局粗分配 + 单仓细排程
- 1 条流式特征链路：Flink/Kafka Streams 产出 `congestion_index`
- 1 套边缘网关：支持 1 万台设备聚合（压测）
- 1 套仿真闭环：仿真引擎 + 回放 + 参数校准

### 19.9 验收标准
- 10 万级状态流压测通过（峰值 QPS 与 Kafka Lag 满足目标）
- 调度 P99 与控制通道 P99 达标（见 19.1）
- 仿真回放与真实数据对齐误差可度量并可调参
