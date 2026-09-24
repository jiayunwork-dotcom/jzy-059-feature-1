package stability

import (
	"fmt"
	"math"
)

// 自由液面修正（Free Surface Correction）。
//
// 物理背景：部分注液的液舱里有一层可自由晃动的液面。船一横倾，舱内液体
// 涌向低侧，全船重心沿横倾方向产生一个虚拟偏移，等价于重心被虚拟抬高，
// 初稳性高度被削掉一截。
//
// 单个液舱造成的虚拟重心抬高量（即对 GM 的正扣减）为：
//
//	δ_i = ρ_i · i_i / Δ
//
// 其中：
//   - ρ_i 是舱内液体密度 (kg/m^3)；
//   - i_i 是自由液面对其自身纵向中线的横向惯性矩 (m^4)，只取决于液面
//     那一层的几何，与舱内装了多少液体无关；
//   - Δ = ρ_w·∇ 是船的排水质量 (kg)。
//
// 全船总扣减为各舱扣减之和，修正后的有效初稳性高度：
//
//	GM_eff = GM_solid − Σδ_i
//
// 两条边界（由本文件的类型与函数结构强制保证）：
//   - 空舱与满舱没有自由液面（满舱液面被舱顶约束、不能晃动），扣减恒为 0；
//   - 扣减只依赖液面几何与液体密度。Tank 结构里刻意没有「装载体积/液位」
//     字段，半满与三分之一满只要液面形状相同，扣减在数值上必然相同。

// 液舱注液状态取值（允许集合）。
const (
	TankStatusEmpty   = "empty"   // 空舱：无液体，无自由液面，扣减为零
	TankStatusPartial = "partial" // 部分注液：存在自由液面，参与修正
	TankStatusFull    = "full"    // 满舱：液面被舱顶约束，不能晃动，扣减为零
)

// Tank 描述一个液舱的自由液面信息。
//
// 液面几何两种给法二选一：
//   - 给矩形液面的长（纵向 l）与宽（横向 b），惯性矩按 l·b³/12 计算；
//   - 直接给 FreeSurfaceInertia（自由液面横向惯性矩 i，m^4）。
//
// 注意：结构中没有液位/装载量字段——自由液面扣减与装了多少液体无关，
// 只有空舱/部分注液/满舱三态之分。
type Tank struct {
	// Name 液舱名，仅用于核算明细与校验报错定位，可省略。
	Name string `json:"name,omitempty"`
	// Length 矩形自由液面的纵向长度 l (m)。与 Width 成对给出。
	Length float64 `json:"length,omitempty"`
	// Width 矩形自由液面的横向宽度 b (m)；横向惯性矩随 b³ 增长，
	// 宽舱对稳性的杀伤远大于窄舱。
	Width float64 `json:"width,omitempty"`
	// FreeSurfaceInertia 直接给定的自由液面横向惯性矩 i (m^4)，
	// 与 Length/Width 二选一，必须为正。
	FreeSurfaceInertia float64 `json:"freeSurfaceInertia,omitempty"`
	// LiquidDensity 舱内液体密度 ρ_i (kg/m^3)，必须为正。
	LiquidDensity float64 `json:"liquidDensity"`
	// Status 注液状态：empty / partial / full。
	Status string `json:"status"`
}

// TankCorrection 是单个液舱的扣减明细，随核算结果一并回报。
type TankCorrection struct {
	// Name 液舱名（可能为空）。
	Name string `json:"name,omitempty"`
	// Index 液舱在请求清单中的下标（从 0 起），用于无名舱定位。
	Index int `json:"index"`
	// Status 该舱的注液状态。
	Status string `json:"status"`
	// FreeSurfaceInertia 本次实际采用的液面惯性矩 (m^4)：
	// 给长宽时由 l·b³/12 推出，直接给时取原值。
	FreeSurfaceInertia float64 `json:"freeSurfaceInertia"`
	// LiquidDensity 舱内液体密度 (kg/m^3)。
	LiquidDensity float64 `json:"liquidDensity"`
	// Correction 该舱对 GM 的扣减量 δ_i (m)，恒非负；空舱/满舱为 0。
	Correction float64 `json:"correction"`
}

// label 给出报错时的舱位定位：「液舱 #3」或「液舱 #3（"燃油舱"）」。
func (t Tank) label(index int) string {
	if t.Name != "" {
		return fmt.Sprintf("液舱 #%d（%q）", index+1, t.Name)
	}
	return fmt.Sprintf("液舱 #%d", index+1)
}

// RectangularSurfaceInertia 矩形自由液面对其自身纵向中线的横向惯性矩：
//
//	i = l·b³/12   (m^4)
//
// l 为纵向长度，b 为横向宽度（三次方项，宽舱主导扣减量）。
func RectangularSurfaceInertia(length, width float64) float64 {
	return length * math.Pow(width, 3) / 12.0
}

// surfaceInertia 解析该舱实际采用的自由液面惯性矩，并把几何层面的
// 非法输入挡在计算之外。
func (t Tank) surfaceInertia(index int) (float64, error) {
	hasDims := t.Length != 0 || t.Width != 0
	hasDirect := t.FreeSurfaceInertia != 0
	switch {
	case hasDims && hasDirect:
		return 0, validationError("%s的自由液面长宽（length/width）与惯性矩（freeSurfaceInertia）只能二选一，不能同时给出", t.label(index))
	case !hasDims && !hasDirect:
		return 0, validationError("%s必须给出矩形自由液面的长宽（length/width），或直接给出自由液面惯性矩（freeSurfaceInertia）", t.label(index))
	case hasDirect:
		if !finite(t.FreeSurfaceInertia) {
			return 0, validationError("%s的自由液面惯性矩必须是有限数值，不接受 NaN 或无穷大", t.label(index))
		}
		if t.FreeSurfaceInertia <= 0 {
			return 0, validationError("%s的自由液面惯性矩必须为正（四次方米），收到 %v", t.label(index), t.FreeSurfaceInertia)
		}
		return t.FreeSurfaceInertia, nil
	default:
		if !finite(t.Length) || !finite(t.Width) {
			return 0, validationError("%s的自由液面长宽必须是有限数值，不接受 NaN 或无穷大", t.label(index))
		}
		if t.Length <= 0 {
			return 0, validationError("%s的自由液面长度 length 必须为正（米），收到 %v", t.label(index), t.Length)
		}
		if t.Width <= 0 {
			return 0, validationError("%s的自由液面宽度 width 必须为正（米），收到 %v", t.label(index), t.Width)
		}
		return RectangularSurfaceInertia(t.Length, t.Width), nil
	}
}

// Validate 校验单个液舱：几何（长宽或惯性矩）须为正、液体密度须为正、
// 注液状态须在允许集合内。空舱/满舱同样要过几何与密度校验——状态只
// 决定扣减是否生效，不豁免参数本身的合法性。
func (t Tank) Validate(index int) error {
	switch t.Status {
	case TankStatusEmpty, TankStatusPartial, TankStatusFull:
	default:
		return validationError("%s的注液状态 status 只能取 %q/%q/%q，收到 %q",
			t.label(index), TankStatusEmpty, TankStatusPartial, TankStatusFull, t.Status)
	}
	if !finite(t.LiquidDensity) {
		return validationError("%s的液体密度必须是有限数值，不接受 NaN 或无穷大", t.label(index))
	}
	if t.LiquidDensity <= 0 {
		return validationError("%s的液体密度必须为正（千克每立方米），收到 %v", t.label(index), t.LiquidDensity)
	}
	if _, err := t.surfaceInertia(index); err != nil {
		return err
	}
	return nil
}

// ValidateTanks 在计算之前逐舱校验整份液舱清单，任一舱不合规即带舱位
// 与字段信息拦下整次核算。
func ValidateTanks(tanks []Tank) error {
	for i, t := range tanks {
		if err := t.Validate(i); err != nil {
			return err
		}
	}
	return nil
}

// TankCorrections 逐舱计算扣减并累加总扣减。
//
// displacementMass 为船的排水质量 Δ = ρ_w∇ (kg)，必须为正。
// 只有 partial 舱产生 δ = ρ_i·i/Δ；empty/full 舱明细照报、扣减为 0。
// 总扣减是各舱扣减的简单算术相加，没有任何耦合项。
func TankCorrections(tanks []Tank, displacementMass float64) ([]TankCorrection, float64, error) {
	if !finite(displacementMass) {
		return nil, 0, validationError("排水质量 Δ 必须是有限数值才能核算自由液面修正")
	}
	if displacementMass <= 0 {
		return nil, 0, validationError("排水质量 Δ = ρ∇ 必须为正才能核算自由液面修正，收到 %v kg", displacementMass)
	}
	details := make([]TankCorrection, 0, len(tanks))
	var total float64
	for i, t := range tanks {
		if err := t.Validate(i); err != nil {
			return nil, 0, err
		}
		inertia, _ := t.surfaceInertia(i)
		d := TankCorrection{
			Name:               t.Name,
			Index:              i,
			Status:             t.Status,
			FreeSurfaceInertia: inertia,
			LiquidDensity:      t.LiquidDensity,
		}
		// 仅部分注液舱有自由液面；空舱/满舱扣减恒为零。
		if t.Status == TankStatusPartial {
			d.Correction = t.LiquidDensity * inertia / displacementMass
		}
		total += d.Correction
		details = append(details, d)
	}
	return details, total, nil
}

// ApplyFreeSurface 是独立的自由液面修正层：在一份「按固体重量分布算好」
// 的核算结果之上叠加修正，不改动 EvaluateAt 的固体计算本身。
//
// 修正后 GZ、复原力矩与稳性判定全部改用有效 GM：
//
//	GM_eff = SolidGM − Σδ_i
//	GZ     = GM_eff·sinφ
//	M      = Δ·g·GZ
func ApplyFreeSurface(solid Result, tanks []Tank) (Result, error) {
	details, total, err := TankCorrections(tanks, solid.DisplacementMass)
	if err != nil {
		return Result{}, err
	}
	out := solid
	out.SolidGM = solid.GM
	out.FreeSurfaceCorrection = total
	out.EffectiveGM = solid.GM - total
	out.TankCorrections = details

	// 之后的力学量一律站在有效 GM 上。
	out.GM = out.EffectiveGM
	out.GZ = out.EffectiveGM * math.Sin(solid.AngleRad)
	out.RightingMoment = solid.DisplacementMass * StandardGravity * out.GZ
	out.Stability = classifyGM(out.EffectiveGM)
	return out, nil
}

// EvaluateLoaded 在单点核算外再挂一组液舱：先按既有路径完成固体核算
// （校验、默认密度、度→弧度、越界提醒全不变），再经独立修正层出结果。
// tanks 为空时与 Evaluate 的结果逐值一致（修正字段为恒等零值）。
func EvaluateLoaded(p Params, tanks []Tank, angleDeg float64) (Result, error) {
	solid, err := Evaluate(p, angleDeg)
	if err != nil {
		return Result{}, err
	}
	if len(tanks) == 0 {
		return solid, nil
	}
	return ApplyFreeSurface(solid, tanks)
}
