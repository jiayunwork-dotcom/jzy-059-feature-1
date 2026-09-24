# 船舶小倾角初稳性核算服务

接收一组浮态几何参数（可再附一组部分注液的液舱），回报初稳性核算结果与随横倾角变化的复原力臂（GZ）曲线。
Go 1.22 + Gin 实现，仅提供 HTTP/JSON 接口，不含前端，与订舱、票务、港口调度等业务无关。

## 核算公式与单位约定

| 量 | 公式 | 单位 |
|---|---|---|
| 横稳心半径 | `BM = IT / ∇` | m |
| 初稳性高度（固体） | `GM_solid = KB + BM − KG` | m |
| 单舱自由液面扣减 | `δ_i = ρ_i · i_i / Δ` | m |
| 自由液面修正总扣减 | `FSC = Σδ_i` | m |
| 有效初稳性高度 | `GM_eff = GM_solid − FSC` | m |
| 复原力臂（小倾角） | `GZ = GM_eff · sin φ` | m |
| 排水质量 | `Δ = ρ∇` | kg |
| 复原力矩 | `M = Δ · g · GZ` | N·m |

- 长度一律 **米**，体积 m³、惯性矩 m⁴、力矩 N·m。
- HTTP 出入参的横倾角一律 **度**；进入三角函数前统一由
  `DegreesToRadians` 换成 **弧度**，响应同时回报 `angleDeg` 与 `angleRad` 便于核对。
- 判定一律落在 **修正后的有效 GM** 上：`GM_eff > 0` → `positive`（自行扶正）；
  `GM_eff < 0` → `negative`（初稳性丧失）；`GM_eff ≈ 0` → `neutral`（临界）。
- 小倾角近似适用范围取 **|φ| ≤ 10°**。超出后仍按同一公式给值，但结果带
  `warning` 越界提醒、`smallAngle` 置 `false`。
- 曲线上每个点都由 `EvaluateLoaded` 按上述公式实算，没有预置正弦曲线。

## 自由液面修正（Free Surface Correction）

部分注液的液舱里有一层可自由晃动的液面：船一横倾，舱内液体涌向低侧，
全船重心沿横倾方向虚拟偏移，等价于重心被抬高、GM 被扣掉一截。修正作为
**独立的一层**（`internal/stability/freesurface.go`）叠加在固体核算之上，
不改动固体计算本身。

- `i_i` 是自由液面对其自身纵向中线的**横向惯性矩**，只取决于液面那一层的
  几何，与舱内装了多少液体无关。矩形液面（纵向长 `l`、横向宽 `b`）：
  `i = l·b³/12`（宽度三次方，宽舱主导扣减量）。也可以直接给
  `freeSurfaceInertia`，两种给法二选一。
- `ρ_i` 是**舱内液体**密度（kg/m³）；分母 `Δ` 是船的排水质量（船外水密度 × ∇）。
- 注液状态三态：`empty`（空舱）、`partial`（部分注液）、`full`（满舱）。
  - 只有 `partial` 舱产生扣减；**空舱与满舱扣减恒为零**（满舱液面被舱顶
    约束、不能晃动）。
  - 扣减只跟液面几何与液体密度有关：**半满和三分之一满，只要液面形状一样，
    扣减就一样**。请求结构中没有液位/装载量字段，从建模上杜绝这个常见错误。
- 各舱各算各的，总扣减是各舱扣减的**简单相加**。
- 修正之后的 `GZ`、复原力矩、正负稳性判定全部改用 `GM_eff`；按固体算的
  `GM_solid` 原值同时回报，挂不挂舱都不变。

## 预置算例（拉起即可手工核对）

矩形驳船 `B=12 m, L=40 m, d=2.5 m, KG=2.0 m`（海水 ρ=1025 kg/m³）：

```
∇  = B·L·d      = 1200 m³
KB = d/2        = 1.25 m
IT = B³L/12     = 5760 m⁴
BM = IT/∇       = 4.8 m
GM = KB+BM−KG   = 4.05 m      （正稳性）
```

服务启动时幂等预置为档名 `rectangular-barge`。

## 运行

```bash
docker build -t ship-stability .          # 构建时自动 go vet + go test -race
docker run -p 8080:8080 -v "$PWD/data:/data" ship-stability
```

环境变量：`PORT`（默认 8080）、`DATA_FILE`（默认 /data/conditions.json）。

本地开发：`go run ./cmd/server`；测试：`go test -race ./...`。

## HTTP 接口

### 单点核算 `POST /api/v1/stability/evaluate`

浮态参数可内联（`params`）或引用已存档名（`conditionName`），二选一。

```json
{
  "conditionName": "rectangular-barge",
  "angleDeg": 10
}
```

返回 `bm / gm / solidGM / freeSurfaceCorrection / effectiveGM / gz /
displacementMass / rightingMoment / stability / angleRad / tankCorrections[]`，
外层另有 `positive` 布尔与 `stability` 标签（均落在修正后的有效 GM 上）。
`gm` 即本次实际采用的 GM：不带舱时等于固体 GM，带舱时等于有效 GM。
内联示例：

```json
{
  "params": {"displacementVolume": 1000, "kb": 1.2, "kg": 2.0,
             "transverseInertia": 5000, "waterDensity": 1025},
  "tanks": [
    {"name": "fuel-oil-tank", "length": 10, "width": 4,
     "liquidDensity": 850, "status": "partial"}
  ],
  "angleDeg": 5
}
```

`waterDensity` 省略或为 0 时取默认海水 1025。`angleDeg` 省略按 0° 处理。

`tanks` 可选，每项字段：

| 字段 | 说明 |
|---|---|
| `name` | 舱名（可选，仅用于明细与报错定位） |
| `length` / `width` | 矩形自由液面的纵向长 / 横向宽（m），与 `freeSurfaceInertia` 二选一 |
| `freeSurfaceInertia` | 直接给自由液面横向惯性矩 `i` (m⁴) |
| `liquidDensity` | 舱内液体密度 (kg/m³)，必须为正 |
| `status` | `empty` / `partial` / `full`，只允许这三个值 |

引用档名且省略 `tanks` 字段时，使用**档案自带液舱**；请求里显式给出
`tanks`（含空数组 `[]`）则以请求为准（`[]` 表示本次临时按无舱核算，不改档案）。

翻盘示例（固体 GM = +0.2 m 的船挂一个 20 m × 10 m 的部分注液宽舱）：

```json
{
  "params": {"displacementVolume": 1000, "kb": 1.2, "kg": 6.0, "transverseInertia": 5000},
  "tanks": [{"name": "wide-wing-tank", "length": 20, "width": 10,
             "liquidDensity": 1000, "status": "partial"}],
  "angleDeg": 5
}
```

返回中 `solidGM = 0.2`、`freeSurfaceCorrection ≈ 1.626`、`effectiveGM ≈ -1.426`，
`stability = "negative"`——纸面上正稳性的船被自由液面拖成负稳性。

### GZ 曲线扫描 `POST /api/v1/stability/scan`

```json
{"conditionName": "rectangular-barge",
 "startDeg": 0, "endDeg": 10, "stepDeg": 2}
```

返回 `bm / gm / solidGM / freeSurfaceCorrection / effectiveGM / stability /
points[]`，每点含 `angleDeg, angleRad, gz, moment, smallAngle`；整条曲线
（含每个点的 GZ/力矩）都建立在修正后的有效 GM 之上；区间越过 10° 时附整体
`warning`。

### 装载状态档

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/conditions` | 列出全部档案 |
| POST | `/api/v1/conditions` | 建档（名字在体内） |
| GET | `/api/v1/conditions/:name` | 取档 |
| PUT | `/api/v1/conditions/:name` | 按路径名建档/覆盖 |
| DELETE | `/api/v1/conditions/:name` | 删档 |

档名仅允许字母、数字、下划线、连字符（1~64 字符）。非法浮态参数（∇ 或 IT 非正、
KB/KG 为负、密度非正、NaN/无穷）一律 400 并带中文 `reason`；档不存在返回 404。

档案体可含 `tanks` 数组，**液舱清单是档案的一部分**：随档案一起建、一起改、
一起删，凭名取回时连同液舱一起还原。液舱参数同样在落库前校验（液面长宽或
惯性矩须为正、两种几何给法不得冲突/全缺、液体密度须为正、`status` 只允许
`empty/partial/full`），任一舱不合规整份档案 400 拦下且不落库，`reason` 指出
具体舱位（如 `液舱 #2（"..."）`）与不合规字段。不同档案的液舱互不串扰。

## 代码结构

```
cmd/server/            进程入口：装配存储、预置算例、启动 HTTP
internal/stability/    稳性内核：公式与校验、横倾扫描、矩形驳船算例、
                       自由液面修正独立层（freesurface.go）
internal/store/        持久化：并发安全的 JSON 文件存储（原子写回）
internal/archive/      装载状态档管理：名字/参数/液舱校验、预置算例
api/                   Gin 路由与 handler
```

## 测试固定的不变关系

- IT 加倍 ⇒ BM、GM 同步增加相同增量；
- KG 抬高 ⇒ GM 线性下降，越过 KB+BM 后判 `negative`；
- 横倾 0° ⇒ GZ、复原力矩恒为零（正、负稳性、挂不挂液舱皆然）；
- 驳船船宽加倍 ⇒ IT 变 8 倍、BM 变 4 倍（∇ 同时翻倍）；
- 改变水密度只改变力矩（N·m）与排水质量，不改变以米计的固体 BM/GM/GZ；
- 专项核对度→弧度换算（与「度直接进 sin」的错误结果明显不同）；
- 非法参数拦截、越界提醒、同名/异名并发计算互不渗透（`-race`）；
- 挂上部分注液舱后 `effectiveGM` 严格低于固体 GM，而 `solidGM` 不因挂舱改变；
- 同一舱在 `partial` 与 `empty`/`full` 间切换，扣减立即归零/恢复；
- 同几何两舱扣减随液体密度增大而增大，总扣减为各舱扣减简单相加；
- 存在固体正稳性、挂舱后有效 GM 转负且判定翻为 `negative` 的端到端算例；
- 空舱/满舱、直给惯性矩与给长宽等价、液舱几何与装载量无关；
- 不带任何液舱时，新路径与原核算逐值一致（向后兼容）。
