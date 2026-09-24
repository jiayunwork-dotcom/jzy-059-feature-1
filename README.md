# 船舶小倾角初稳性核算服务

接收一组浮态几何参数，回报初稳性核算结果与随横倾角变化的复原力臂（GZ）曲线。
Go 1.22 + Gin 实现，仅提供 HTTP/JSON 接口，不含前端，与订舱、票务、港口调度等业务无关。

## 核算公式与单位约定

| 量 | 公式 | 单位 |
|---|---|---|
| 横稳心半径 | `BM = IT / ∇` | m |
| 初稳性高度（固体） | `GM_solid = KB + BM − KG` | m |
| 单舱自由液面扣减 | `δGMᵢ = ρᵢ·i / (ρ∇)` | m |
| 自由液面总扣减 | `δGM = Σ δGMᵢ`（仅部分注液舱） | m |
| 有效初稳性高度 | `GM_eff = GM_solid − δGM` | m |
| 复原力臂（小倾角） | `GZ = GM_eff · sin φ` | m |
| 排水质量 | `Δ = ρ∇` | kg |
| 复原力矩 | `M = Δ · g · GZ` | N·m |

### 自由液面修正

部分注液舱内的液面随横倾自由晃动，等效于把全船重心抬高、压低初稳性高度。
修正作为**独立一层**叠加在固体核算之上（见 `internal/stability/freesurface.go`）：

- `i` 为自由液面那一层对其自身纵向中线的横向惯性矩 (m⁴)；矩形液面
  `i = l·b³/12`，非矩形液面可直接给出 `freeSurfaceInertia`。
- `ρᵢ` 为**舱内**液体密度；`ρ∇` 为船的排水质量（`ρ` 是船外水密度）。
- 只有 `fillingStatus = "partial"`（部分注液）的舱参与扣减；
  `"empty"`（空舱）与 `"full"`（满舱）液面不能自由晃动，扣减恒为零。
- 扣减只取决于**液面几何**与舱内液体密度，与舱内现有液体体积无关：
  半满与三分之一满只要液面形状相同，扣减就相同（数据结构里根本没有装液量字段）。
- 挂舱后 `gm`、`gz`、复原力矩与稳性判定全部改用 `GM_eff`；响应同时回报
  `solidGm`（固体 GM）、`freeSurfaceCorrection`（总扣减）、
  `effectiveGm`（= `gm`）与逐舱 `tankCorrections`。
- 不带任何液舱时，响应不含上述字段，结果与纯固体核算逐字节一致。

- 长度一律 **米**，体积 m³、惯性矩 m⁴、力矩 N·m。
- HTTP 出入参的横倾角一律 **度**；进入三角函数前统一由
  `DegreesToRadians` 换成 **弧度**，响应同时回报 `angleDeg` 与 `angleRad` 便于核对。
- 判定：`GM_eff > 0` → `positive`（自行扶正）；`GM_eff < 0` → `negative`
  （初稳性丧失）；`GM_eff ≈ 0` → `neutral`（临界）。判定始终落在**修正后**的
  有效 GM 上：固体 GM 为正的船，挂上足够多、足够宽的部分注液舱后可以被判负。
- 小倾角近似适用范围取 **|φ| ≤ 10°**。超出后仍按同一公式给值，但结果带
  `warning` 越界提醒、`smallAngle` 置 `false`。
- 曲线上每个点都由 `EvaluateAt` 按上述公式实算，没有预置正弦曲线。

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

返回 `bm / gm / gz / displacementMass / rightingMoment / stability / angleRad`，
外层另有 `positive` 布尔与 `stability` 标签。内联示例：

```json
{
  "params": {"displacementVolume": 1000, "kb": 1.2, "kg": 2.0,
             "transverseInertia": 5000, "waterDensity": 1025},
  "angleDeg": 5
}
```

`waterDensity` 省略或为 0 时取默认海水 1025。`angleDeg` 省略按 0° 处理。

可随请求附一组液舱 `tanks[]`，每个舱给出自由液面的长宽（矩形液面）或直接给
`freeSurfaceInertia`、舱内液体密度 `liquidDensity` 与注液状态
`fillingStatus`（`empty` / `partial` / `full`）：

```json
{
  "params": {"displacementVolume": 1000, "kb": 1.2, "kg": 2.0,
             "transverseInertia": 5000, "waterDensity": 1025},
  "angleDeg": 5,
  "tanks": [
    {"name": "fuel-1", "length": 4, "width": 2,
     "liquidDensity": 900, "fillingStatus": "partial"},
    {"name": "ballast-1", "freeSurfaceInertia": 13.5,
     "liquidDensity": 1025, "fillingStatus": "full"}
  ]
}
```

返回的 `result` 在原有字段之外增加 `solidGm / freeSurfaceCorrection /
effectiveGm / tankCorrections[]`；其中 `gm/gz/rightingMoment/stability`
全部按修正后的有效 GM 计算。空舱与满舱在 `tankCorrections` 里扣减为 0。

### GZ 曲线扫描 `POST /api/v1/stability/scan`

```json
{"conditionName": "rectangular-barge",
 "startDeg": 0, "endDeg": 10, "stepDeg": 2}
```

返回 `bm / gm / stability / points[]`，每点含
`angleDeg, angleRad, gz, moment, smallAngle`；区间越过 10° 时附整体 `warning`。
扫描同样接受 `tanks[]`：头部增加 `solidGm / freeSurfaceCorrection /
effectiveGm / tankCorrections[]`，**整条 GZ 曲线都建立在有效 GM 之上**，
设计者直接看到考虑液舱晃动后的曲线，而不是刚体乐观版本。

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

档案可携带 `tanks[]` 液舱清单，作为档案的一部分随它**一起建、一起取、一起改、
一起删**：凭名字取回时连同液舱一并还原，evaluate / scan 直接按档案内的舱做修正；
不同档案的舱互不串扰，覆盖档案即整体替换舱单，不带 `tanks` 覆盖即清空。
液舱参数在落库前校验：液面长宽或惯性矩必须为正、`fillingStatus` 必须取自
`empty/partial/full`、`liquidDensity` 必须为正，非法时 400 并指明是第几个舱、
哪个字段，整份档案不会落库。

引用档名时液舱以档案登记为准（请求体内再给 `tanks` 会被拒绝）；要临时挂舱试算，
请改用内联 `params + tanks`。

## 代码结构

```
cmd/server/            进程入口：装配存储、预置算例、启动 HTTP
internal/stability/    稳性内核：公式与校验、自由液面修正层、横倾扫描、矩形驳船算例
internal/store/        持久化：并发安全的 JSON 文件存储（原子写回，含液舱记录）
internal/archive/      装载状态档管理：名字/参数/液舱校验、预置算例
api/                   Gin 路由与 handler
```

## 测试固定的不变关系

- IT 加倍 ⇒ BM、GM 同步增加相同增量；
- KG 抬高 ⇒ GM 线性下降，越过 KB+BM 后判 `negative`；
- 横倾 0° ⇒ GZ、复原力矩恒为零（正、负稳性皆然）；
- 驳船船宽加倍 ⇒ IT 变 8 倍、BM 变 4 倍（∇ 同时翻倍）；
- 改变水密度只改变力矩（N·m）与排水质量，不改变以米计的 BM/GM/GZ；
- 专项核对度→弧度换算（与「度直接进 sin」的错误结果明显不同）；
- 非法参数拦截、越界提醒、同名/异名并发计算互不渗透（`-race`）。

自由液面修正固定的不变关系：

- 挂一个部分注液舱后有效 GM **严格低于**不挂舱的固体 GM，而固体 GM 本身不改变；
- 同一舱从 `partial` 改成 `empty`/`full`，扣减立刻归零；
- 几何相同的两舱，液体密度更大者扣减更大；总扣减是各舱扣减的简单相加（与顺序无关）；
- 扣减只由液面几何与舱内密度决定，与装液体积无关；
- 固体 GM 为正、挂舱后有效 GM 转负的算例，最终判定确实翻成 `negative`，
  同批舱灌满后回到 `positive`；
- 横倾 0° 时 GZ 恒为零，不因引入液舱而改变；
- 液舱随档案存取，改 A 船的舱不污染 B 船；非法舱在计算/落库前被拦下并指出舱号；
- 不带液舱时，evaluate / scan 的请求与响应行为与纯固体版本完全一致。
