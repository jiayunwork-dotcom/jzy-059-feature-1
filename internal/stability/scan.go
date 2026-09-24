package stability

import (
	"fmt"
	"math"
)

// MaxScanPoints 单次扫描允许返回的最大点数，防止步长过小拖垮服务。
const MaxScanPoints = 10000

// ScanParams 描述横倾角扫描区间，角度均以「度」计。
type ScanParams struct {
	Params
	// Tanks 随船携带的液舱清单；为 nil/空时按理想刚体扫描，
	// 结果与不引入自由液面前完全一致。
	Tanks []Tank
	// StartDeg 起始横倾角（度）。
	StartDeg float64 `json:"startDeg"`
	// EndDeg 终止横倾角（度），须不小于 StartDeg。
	EndDeg float64 `json:"endDeg"`
	// StepDeg 采样步长（度），必须为正。
	StepDeg float64 `json:"stepDeg"`
}

// CurvePoint 是复原力臂曲线上的一个采样点，全部由公式实算得到。
type CurvePoint struct {
	AngleDeg   float64 `json:"angleDeg"`
	AngleRad   float64 `json:"angleRad"`
	GZ         float64 `json:"gz"`         // 复原力臂 (m)
	Moment     float64 `json:"moment"`     // 复原力矩 (N·m)
	SmallAngle bool    `json:"smallAngle"` // 该点是否仍在 10° 小倾角范围内
}

// ScanResult 是一次扫描的完整结果。
type ScanResult struct {
	BM float64 `json:"bm"`
	// GM 曲线上实际使用的初稳性高度：带液舱时为有效 GM。
	GM float64 `json:"gm"`
	// SolidGM 按固体算的初稳性高度，与是否挂舱无关。
	SolidGM float64 `json:"solidGM"`
	// FreeSurfaceCorrection 自由液面修正总扣减 (m)。
	FreeSurfaceCorrection float64 `json:"freeSurfaceCorrection"`
	// EffectiveGM 修正后的有效初稳性高度 = SolidGM − 扣减 (m)。
	EffectiveGM float64 `json:"effectiveGM"`
	// TankCorrections 各液舱扣减明细；无液舱时省略。
	TankCorrections []TankCorrection `json:"tankCorrections,omitempty"`
	// Stability 以修正后有效 GM 给出的初稳性判定。
	Stability string       `json:"stability"`
	Points    []CurvePoint `json:"points"`
	// Warning 非空时表示扫描区间越过了小倾角范围；每个超界点
	// 的 smallAngle 字段同时置 false，可逐点判断。
	Warning string `json:"warning,omitempty"`
}

// Validate 校验扫描参数：浮态参数须合法，区间与步长也须自洽。
func (s ScanParams) Validate() error {
	np, err := s.Params.Normalize()
	if err != nil {
		return err
	}
	if err := np.Validate(); err != nil {
		return err
	}
	if err := ValidateTanks(s.Tanks); err != nil {
		return err
	}
	if !finite(s.StartDeg) || !finite(s.EndDeg) || !finite(s.StepDeg) {
		return validationError("扫描区间的起点、终点与步长必须是有限数值（度）")
	}
	if s.StepDeg <= 0 {
		return validationError("扫描步长必须为正（度），收到 %v", s.StepDeg)
	}
	if s.EndDeg < s.StartDeg {
		return validationError("扫描终止角 %v° 小于起始角 %v°", s.EndDeg, s.StartDeg)
	}
	count := 1 + int(math.Floor((s.EndDeg-s.StartDeg)/s.StepDeg+1e-9))
	if count > MaxScanPoints {
		return validationError("扫描点数 %d 超过上限 %d，请放大步长或缩小区间", count, MaxScanPoints)
	}
	return nil
}

// Scan 在 [StartDeg, EndDeg] 上按固定步长采样，等价于 ScanLoaded 且
// 不带任何液舱。保留原签名，保证既有调用方与理想刚体路径结果不变。
func Scan(s ScanParams) (ScanResult, error) {
	s.Tanks = nil
	return ScanLoaded(s)
}

// ScanLoaded 与 Scan 相同，但整条复原力臂曲线建立在自由液面修正后的
// 有效初稳性高度之上：每个点都经 EvaluateLoaded 实算，头部的 GM/判定
// 同样落在修正后的值上。
func ScanLoaded(s ScanParams) (ScanResult, error) {
	if err := s.Validate(); err != nil {
		return ScanResult{}, err
	}
	np, _ := s.Params.Normalize()

	first, err := EvaluateLoaded(np, s.Tanks, s.StartDeg)
	if err != nil {
		return ScanResult{}, err
	}
	out := ScanResult{
		BM:                    first.BM,
		GM:                    first.GM,
		SolidGM:               first.SolidGM,
		FreeSurfaceCorrection: first.FreeSurfaceCorrection,
		EffectiveGM:           first.EffectiveGM,
		TankCorrections:       first.TankCorrections,
		Stability:             first.Stability,
	}

	count := 1 + int(math.Floor((s.EndDeg-s.StartDeg)/s.StepDeg+1e-9))
	out.Points = make([]CurvePoint, 0, count)

	rangeExceedsSmall := false
	for i := 0; i < count; i++ {
		angle := s.StartDeg + float64(i)*s.StepDeg
		// 修正浮点漂移：理论末点直接取 EndDeg，保证终止角一定被采到。
		if i == count-1 && math.Abs(angle-s.EndDeg) < 1e-9*math.Max(1, math.Abs(s.EndDeg)) {
			angle = s.EndDeg
		}
		r, err := EvaluateLoaded(np, s.Tanks, angle)
		if err != nil {
			return ScanResult{}, err
		}
		if !r.SmallAngle {
			rangeExceedsSmall = true
		}
		out.Points = append(out.Points, CurvePoint{
			AngleDeg:   angle,
			AngleRad:   r.AngleRad,
			GZ:         r.GZ,
			Moment:     r.RightingMoment,
			SmallAngle: r.SmallAngle,
		})
	}

	if rangeExceedsSmall {
		out.Warning = fmt.Sprintf(
			"扫描区间 [%.3f°, %.3f°] 越过小倾角近似适用范围（%g° 以内）；"+
				"超出部分的 GZ = GM·sinφ 结果仅供参考",
			s.StartDeg, s.EndDeg, SmallAngleLimitDeg)
	}
	return out, nil
}
