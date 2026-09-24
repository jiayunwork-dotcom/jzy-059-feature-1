# 船舶小倾角初稳性核算服务

接收一组浮态几何参数，回报初稳性核算结果与随横倾角变化的复原力臂（GZ）曲线。
Go 1.22 + Gin 实现，仅提供 HTTP/JSON 接口，不含前端，与订舱、票务、港口调度等业务无关。

## 核算公式与单位约定

| 量 | 公式 | 单位 |
|---|---|---|
| 横稳心半径 | `BM = IT / ∇` | m |
| 初稳性高度 | `GM = KB + BM − KG` | m |
| 复原力臂（小倾角） | `GZ = GM · sin φ` | m |
| 排水质量 | `Δ = ρ∇` | kg |
| 复原力矩 | `M = Δ · g · GZ` | N·m |

- 长度一律 **米**，体积 m³、惯性矩 m⁴、力矩 N·m。
- HTTP 出入参的横倾角一律 **度**；进入三角函数前统一由
  `DegreesToRadians` 换成 **弧度**，响应同时回报 `angleDeg` 与 `angleRad` 便于核对。
- 判定：`GM > 0` → `positive`（自行扶正）；`GM < 0` → `negative`（初稳性丧失）；
  `GM ≈ 0` → `neutral`（临界）。
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

### GZ 曲线扫描 `POST /api/v1/stability/scan`

```json
{"conditionName": "rectangular-barge",
 "startDeg": 0, "endDeg": 10, "stepDeg": 2}
```

返回 `bm / gm / stability / points[]`，每点含
`angleDeg, angleRad, gz, moment, smallAngle`；区间越过 10° 时附整体 `warning`。

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

## 代码结构

```
cmd/server/            进程入口：装配存储、预置算例、启动 HTTP
internal/stability/    稳性内核：公式与校验、横倾扫描、矩形驳船算例
internal/store/        持久化：并发安全的 JSON 文件存储（原子写回）
internal/archive/      装载状态档管理：名字/参数校验、预置算例
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
