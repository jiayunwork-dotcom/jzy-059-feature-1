package stability

import (
	"fmt"
	"math"
)

// 液舱注液状态取值。只有部分注液（partial）时液面才能自由晃动，
// 空舱（empty）与满舱（full）都不存在自由液面，扣减恒为零。
const (
	TankStatusEmpty   = "empty"   // 空舱：无液体，无自由液面
	TankStatusPartial = "partial" // 部分注液：存在可自由晃动的液面
	TankStatusFull    = "full"    // 满舱：液体灌满，液面无法晃动
)

// Tank 描述一个部分注液（或空/满）的液舱。
//
// 自由液面修正只取决于「液面那一层」的几何，与舱内现有液体体积无关：
// 半满与三分之一满，只要液面形状相同，扣减就相同——因此这里根本没有
// 「装液量」字段，从类型上杜绝把装液体积混进扣减的写法。
type Tank struct {
	// Name 舱名，仅用于报错定位与修正明细展示，可省略。
	Name string `json:"name,omitempty"`
	// Length 自由液面沿船长方向的长度 (m)。
	Length float64 `json:"length"`
	// Width 自由液面沿船宽方向的宽度 (m)。矩形液面绕自身纵向
	// 中线的横向惯性矩 i = Length·Width³/12。
	Width float64 `json:"width"`
	// FreeSurfaceInertia 自由液面横向惯性矩 i (m^4)。为正时优先使用，
	// 非矩形液面可直接给出该值；为 0 时由长宽按矩形公式计算。
	FreeSurfaceInertia float64 `json:"freeSurfaceInertia"`
	// LiquidDensity 舱内液体密度 ρᵢ (kg/m^3)，必须为正。
	LiquidDensity float64 `json:"liquidDensity"`
	// FillingStatus 注液状态：empty / partial / full。
	FillingStatus string `json:"fillingStatus"`
}

// TankCorrection 是单个液舱的修正明细。
type TankCorrection struct {
	// Name 舱名（可能为空）。
	Name string `json:"name,omitempty"`
	// FillingStatus 该舱核算时的注液状态。
	FillingStatus string `json:"fillingStatus"`
	// SurfaceInertia 液面几何横向惯性矩 i (m^4)，由长宽算得或直接给出。
	// 空舱/满舱时几何仍如实回报，但 Correction 为零。
	SurfaceInertia float64 `json:"surfaceInertia"`
	// LiquidDensity 舱内液体密度 (kg/m^3)。
	LiquidDensity float64 `json:"liquidDensity"`
	// Correction 该舱对 GM 的扣减量 δGM = ρᵢ·i/(ρ∇) (m)，
	// 空舱/满舱恒为零。
	Correction float64 `json:"correction"`
}

// FreeSurfaceSummary 是自由液面修正这一独立层的计算结果。
type FreeSurfaceSummary struct {
	// SolidGM 按固体重量分布算出的初稳性高度（未经修正）(m)。
	SolidGM float64 `json:"solidGm"`
	// TotalCorrection 全部部分注液舱扣减量之和 (m)，恒为非负。
	TotalCorrection float64 `json:"totalCorrection"`
	// EffectiveGM 修正后的有效初稳性高度 = SolidGM − TotalCorrection (m)。
	EffectiveGM float64 `json:"effectiveGm"`
	// Tanks 逐舱明细，顺序与输入一致。
	Tanks []TankCorrection `json:"tanks"`
}

// tankLabel 供报错定位：#序号（舱名）。
func tankLabel(index int, name string) string {
	if name == "" {
		return fmt.Sprintf("#%d", index+1)
	}
	return fmt.Sprintf("#%d（%s）", index+1, name)
}

// surfaceInertia 取该舱液面几何的横向惯性矩：
// 显式给出正值惯性矩时以其为准，否则按矩形 i = l·b³/12 计算。
// ok 为 false 表示仅凭现有字段无法确定液面几何。
func (t Tank) surfaceInertia() (float64, bool) {
	if t.FreeSurfaceInertia != 0 {
		return t.FreeSurfaceInertia, true
	}
	if t.Length != 0 && t.Width != 0 {
		return t.Length * math.Pow(t.Width, 3) / 12.0, true
	}
	return 0, false
}

// Validate 校验单个液舱：状态取值合法、密度为正、液面几何（长宽或
// 直接给出的惯性矩）为正、所有数值有限。空舱/满舱同样要过这些校验——
// 能参与计算的舱必须先是一个描述完整的舱。
func (t Tank) Validate(index int) error {
	label := tankLabel(index, t.Name)
	if !finite(t.Length) || !finite(t.Width) ||
		!finite(t.FreeSurfaceInertia) || !finite(t.LiquidDensity) {
		return validationError("液舱 %s 的参数必须是有限数值，不接受 NaN 或无穷大", label)
	}
	switch t.FillingStatus {
	case TankStatusEmpty, TankStatusPartial, TankStatusFull:
	default:
		return validationError(
			"液舱 %s 的注液状态 %q 不合法，只允许 %q（空舱）、%q（部分注液）、%q（满舱）",
			label, t.FillingStatus, TankStatusEmpty, TankStatusPartial, TankStatusFull)
	}
	if t.LiquidDensity <= 0 {
		return validationError("液舱 %s 的舱内液体密度必须为正（千克每立方米），收到 %v",
			label, t.LiquidDensity)
	}
	if t.FreeSurfaceInertia < 0 {
		return validationError("液舱 %s 的自由液面惯性矩必须为正（四次方米），收到 %v",
			label, t.FreeSurfaceInertia)
	}
	if t.Length < 0 || t.Width < 0 {
		return validationError("液舱 %s 的自由液面长、宽不得为负（米），收到 length=%v width=%v",
			label, t.Length, t.Width)
	}
	if t.FreeSurfaceInertia == 0 && !(t.Length > 0 && t.Width > 0) {
		return validationError(
			"液舱 %s 必须给出正的自由液面长与宽（米），或直接给出正的自由液面惯性矩（四次方米）；"+
				"收到 length=%v width=%v inertia=%v",
			label, t.Length, t.Width, t.FreeSurfaceInertia)
	}
	return nil
}

// ValidateTanks 逐舱校验，返回的错误带舱序号与字段信息。
func ValidateTanks(tanks []Tank) error {
	for i, t := range tanks {
		if err := t.Validate(i); err != nil {
			return err
		}
	}
	return nil
}

// FreeSurfaceCorrection 是自由液面修正的独立一层：
//
//	排水质量 Δ = ρ∇（ρ 为船外水密度）
//	单舱扣减 δGM = ρᵢ·i / Δ   （仅 partial 舱；empty/full 为 0）
//	总扣减    δGM_total = Σ δGM
//	有效 GM   GM_eff = GM_solid − δGM_total
//
// 扣减只与液面几何 i 和舱内液体密度 ρᵢ 有关，与装液体积无关。
// 入参浮态参数会先经 Normalize/Validate；任一液舱不合法同样挡在计算之外。
func FreeSurfaceCorrection(p Params, tanks []Tank) (FreeSurfaceSummary, error) {
	np, err := p.Normalize()
	if err != nil {
		return FreeSurfaceSummary{}, err
	}
	if err := np.Validate(); err != nil {
		return FreeSurfaceSummary{}, err
	}
	if err := ValidateTanks(tanks); err != nil {
		return FreeSurfaceSummary{}, err
	}

	solidGM := np.KB + np.TransverseInertia/np.DisplacementVolume - np.KG
	displacementMass := np.WaterDensity * np.DisplacementVolume

	summary := FreeSurfaceSummary{
		SolidGM: solidGM,
		Tanks:   make([]TankCorrection, 0, len(tanks)),
	}
	for _, t := range tanks {
		i, _ := t.surfaceInertia() // Validate 已保证几何确定
		detail := TankCorrection{
			Name:           t.Name,
			FillingStatus:  t.FillingStatus,
			SurfaceInertia: i,
			LiquidDensity:  t.LiquidDensity,
		}
		// 两条边界：空舱与满舱液面不自由晃动，扣减为零；扣减与装液体积无关。
		if t.FillingStatus == TankStatusPartial {
			detail.Correction = t.LiquidDensity * i / displacementMass
			summary.TotalCorrection += detail.Correction
		}
		summary.Tanks = append(summary.Tanks, detail)
	}
	summary.EffectiveGM = summary.SolidGM - summary.TotalCorrection
	return summary, nil
}

// applyFreeSurface 把修正层结果落到一份单点核算结果上：GM 换成有效 GM，
// GZ 与复原力矩全部按有效 GM 重算，稳性判定也以有效 GM 为准。
// BM、排水质量、角度、小倾角标记与越界提醒保持不变。
func applyFreeSurface(base Result, s FreeSurfaceSummary) Result {
	out := base
	out.SolidGM = s.SolidGM
	out.FreeSurfaceCorrection = s.TotalCorrection
	out.EffectiveGM = s.EffectiveGM
	out.TankCorrections = s.Tanks
	out.GM = s.EffectiveGM
	out.GZ = s.EffectiveGM * math.Sin(base.AngleRad)
	out.RightingMoment = base.DisplacementMass * StandardGravity * out.GZ
	out.Stability = classifyGM(s.EffectiveGM)
	return out
}

// EvaluateLoading 在单点核算之上叠加自由液面修正层。
// tanks 为空（不含液舱维度）时行为与 Evaluate 完全一致，结果字节级兼容；
// 只要携带液舱，返回的 GM/GZ/力矩/稳性判定就全部建立在修正后的有效 GM 上，
// 并额外回报 solidGm、freeSurfaceCorrection 与逐舱扣减明细。
func EvaluateLoading(p Params, tanks []Tank, angleDeg float64) (Result, error) {
	base, err := Evaluate(p, angleDeg)
	if err != nil {
		return Result{}, err
	}
	if len(tanks) == 0 {
		return base, nil
	}
	summary, err := FreeSurfaceCorrection(p, tanks)
	if err != nil {
		return Result{}, err
	}
	return applyFreeSurface(base, summary), nil
}
