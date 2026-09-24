// Package stability 实现小倾角初稳性核算的纯数学内核。
//
// 单位约定（全服务统一，务必分清）：
//   - 长度量：米 (m)，KB、KG、BM、GM、GZ 均以米计；
//   - 体积量：立方米 (m^3)，排水体积 ∇、水线面横向惯性矩 IT 以 m^4 计；
//   - 角度量：对外 HTTP 一律用「度」，进入三角函数前由 DegreesToRadians
//     转成「弧度」，内核内部只使用弧度；
//   - 质量量：千克 (kg)，排水质量 Δ = ρ∇；
//   - 力矩量：牛顿·米 (N·m)，复原力矩 = ρ∇·g·GZ。
package stability

import (
	"fmt"
	"math"
)

const (
	// StandardGravity 标准重力加速度 (m/s^2)。
	StandardGravity = 9.80665
	// DefaultWaterDensity 默认水密度，取标准海水 (kg/m^3)。
	// 调用方显式给出正值时以调用方为准（淡水可取 1000）。
	DefaultWaterDensity = 1025.0
	// SmallAngleLimitDeg 小倾角近似的适用上限（度）。
	// φ 严格大于该角度仍按 GZ = GM·sinφ 给值，但结果附带越界提醒。
	SmallAngleLimitDeg = 10.0
)

// ValidationError 表示输入参数未通过物理校验，被挡在计算之外。
type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string { return e.Reason }

func validationError(format string, args ...any) error {
	return &ValidationError{Reason: fmt.Sprintf(format, args...)}
}

// Params 是一组浮态几何参数（角度不在其中，按次计算时给出）。
type Params struct {
	// DisplacementVolume 排水体积 ∇ (m^3)，必须为正。
	DisplacementVolume float64 `json:"displacementVolume"`
	// KB 浮心距基线的竖向高度 (m)，不得为负。
	KB float64 `json:"kb"`
	// KG 重心距基线的竖向高度 (m)，不得为负。
	KG float64 `json:"kg"`
	// TransverseInertia 水线面对纵中剖面的横向惯性矩 IT (m^4)，必须为正。
	TransverseInertia float64 `json:"transverseInertia"`
	// WaterDensity 水密度 ρ (kg/m^3)。为 0 时取 DefaultWaterDensity；不得为负。
	WaterDensity float64 `json:"waterDensity"`
}

// Result 是单个横倾角下的稳性核算结果。
type Result struct {
	// AngleDeg 横倾角（度），即请求时给出的角度。
	AngleDeg float64 `json:"angleDeg"`
	// AngleRad 横倾角（弧度），真正参与三角函数的值，用于核对单位换算。
	AngleRad float64 `json:"angleRad"`
	// BM 横稳心半径 BM = IT/∇ (m)。
	BM float64 `json:"bm"`
	// GM 初稳性高度 GM = KB + BM − KG (m)。
	GM float64 `json:"gm"`
	// GZ 复原力臂 GZ = GM·sinφ (m)，φ 以弧度代入。
	GZ float64 `json:"gz"`
	// DisplacementMass 排水质量 Δ = ρ∇ (kg)。
	DisplacementMass float64 `json:"displacementMass"`
	// RightingMoment 复原力矩 = Δ·g·GZ (N·m)。
	RightingMoment float64 `json:"rightingMoment"`
	// Stability 初稳性判定：positive / negative / neutral。
	Stability string `json:"stability"`
	// SmallAngle 该点是否仍在小倾角近似范围内（|φ| ≤ 10°）。
	SmallAngle bool `json:"smallAngle"`
	// Warning 非空时表示本次计算存在需要设计者注意的事项。
	Warning string `json:"warning,omitempty"`
}

// 初稳性判定标签。
const (
	StabilityPositive = "positive" // GM 为正，受扰后自行扶正
	StabilityNegative = "negative" // GM 为负，初稳性丧失
	StabilityNeutral  = "neutral"  // GM 恰为零（临界，工程上按不稳处理）
)

// gmEpsilon 用于吸收浮点误差：|GM| 小于该值按中性稳性处理。
const gmEpsilon = 1e-12

// DegreesToRadians 把角度从度换成弧度。横倾角只有换成弧度后才能
// 进入 math.Sin；省掉这一步，10° 附近的 GZ 会显著偏大。
func DegreesToRadians(deg float64) float64 {
	return deg * math.Pi / 180.0
}

// Normalize 规整参数：补默认水密度，并检查是否为有限数。
// 不做物理取值范围校验，那是 Validate 的职责。
func (p Params) Normalize() (Params, error) {
	out := p
	if out.WaterDensity == 0 {
		out.WaterDensity = DefaultWaterDensity
	}
	if !finite(out.DisplacementVolume) || !finite(out.KB) || !finite(out.KG) ||
		!finite(out.TransverseInertia) || !finite(out.WaterDensity) {
		return out, validationError("参数必须是有限数值，不接受 NaN 或无穷大")
	}
	return out, nil
}

// Validate 按物理约定校验浮态参数，不合法即返回带原因的错误。
func (p Params) Validate() error {
	if p.DisplacementVolume <= 0 {
		return validationError("排水体积 ∇ 必须为正（立方米），收到 %v", p.DisplacementVolume)
	}
	if p.TransverseInertia <= 0 {
		return validationError("水线面横向惯性矩 IT 必须为正（四次方米），收到 %v", p.TransverseInertia)
	}
	if p.KB < 0 {
		return validationError("浮心高度 KB 不得为负（米），收到 %v", p.KB)
	}
	if p.KG < 0 {
		return validationError("重心高度 KG 不得为负（米），收到 %v", p.KG)
	}
	if p.WaterDensity <= 0 {
		return validationError("水密度 ρ 必须为正（千克每立方米），收到 %v", p.WaterDensity)
	}
	return nil
}

// classifyGM 按 GM 正负给出初稳性判定。
func classifyGM(gm float64) string {
	switch {
	case gm > gmEpsilon:
		return StabilityPositive
	case gm < -gmEpsilon:
		return StabilityNegative
	default:
		return StabilityNeutral
	}
}

// EvaluateAt 是内核共享的单点计算：给定规整后的合法参数与一个「弧度」
// 横倾角，实算 BM/GM/GZ/排水质量/复原力矩。扫描曲线上的每一个点都
// 必须经过这里，禁止另写一条正弦曲线糊弄。
//
// 注意：phiRad 必须是弧度。对外接口拿到的角度（度）应先经
// DegreesToRadians 转换后再调用本函数。
func EvaluateAt(p Params, phiRad float64) Result {
	bm := p.TransverseInertia / p.DisplacementVolume
	gm := p.KB + bm - p.KG
	gz := gm * math.Sin(phiRad)
	mass := p.WaterDensity * p.DisplacementVolume
	moment := mass * StandardGravity * gz

	return Result{
		AngleRad:         phiRad,
		BM:               bm,
		GM:               gm,
		GZ:               gz,
		DisplacementMass: mass,
		RightingMoment:   moment,
		Stability:        classifyGM(gm),
	}
}

// Evaluate 给定一组浮态参数与横倾角（度），完成校验、单位换算与
// 单点核算。角度超过 10° 时仍按小倾角公式给值，但会在 Result.Warning
// 中附上越界提醒。
func Evaluate(p Params, angleDeg float64) (Result, error) {
	if !finite(angleDeg) {
		return Result{}, validationError("横倾角必须是有限数值（度）")
	}
	np, err := p.Normalize()
	if err != nil {
		return Result{}, err
	}
	if err := np.Validate(); err != nil {
		return Result{}, err
	}

	// 度 → 弧度，之后只与弧度打交道。
	phiRad := DegreesToRadians(angleDeg)
	r := EvaluateAt(np, phiRad)
	r.AngleDeg = angleDeg
	r.SmallAngle = math.Abs(angleDeg) <= SmallAngleLimitDeg
	if !r.SmallAngle {
		r.Warning = fmt.Sprintf(
			"横倾角 %.3f° 已超出小倾角近似适用范围（%g° 以内）；GZ = GM·sinφ 的线性结果仅供参考，"+
				"大倾角应改用完整稳性横交曲线核算",
			angleDeg, SmallAngleLimitDeg)
	}
	return r, nil
}

func finite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}
