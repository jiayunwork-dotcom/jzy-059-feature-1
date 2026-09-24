package stability

import "math"

// BargeGeometry 描述一条等截面矩形驳船的主尺度（米）。
// 假定正浮、船体为长方体、水线面为 B×L 的矩形。
type BargeGeometry struct {
	Beam   float64 `json:"beam"`   // 船宽 B (m)
	Length float64 `json:"length"` // 船长 L (m)
	Draft  float64 `json:"draft"`  // 吃水 d (m)
	KG     float64 `json:"kg"`     // 重心距基线高度 (m)
}

// Validate 校验驳船主尺度是否为正、KG 是否非负。
func (b BargeGeometry) Validate() error {
	if !finite(b.Beam) || !finite(b.Length) || !finite(b.Draft) || !finite(b.KG) {
		return validationError("驳船主尺度必须是有限数值")
	}
	if b.Beam <= 0 || b.Length <= 0 || b.Draft <= 0 {
		return validationError("驳船船宽、船长与吃水都必须为正（米）")
	}
	if b.KG < 0 {
		return validationError("驳船重心高度 KG 不得为负（米），收到 %v", b.KG)
	}
	return nil
}

// BargeWaterplaneInertia 矩形水线面对纵中剖面的横向惯性矩 IT = B³L/12 (m^4)。
func BargeWaterplaneInertia(beam, length float64) float64 {
	return math.Pow(beam, 3) * length / 12.0
}

// BargeDisplacementVolume 长方体排水体积 ∇ = B·L·d (m^3)。
func BargeDisplacementVolume(beam, length, draft float64) float64 {
	return beam * length * draft
}

// ToParams 由驳船主尺度推出浮态参数：
//
//	∇ = B·L·d，KB = d/2（矩形截面正浮时浮心位于吃水一半处），
//	IT = B³L/12，水密度取默认海水值（可由调用方覆盖）。
func (b BargeGeometry) ToParams() (Params, error) {
	if err := b.Validate(); err != nil {
		return Params{}, err
	}
	p := Params{
		DisplacementVolume: BargeDisplacementVolume(b.Beam, b.Length, b.Draft),
		KB:                 b.Draft / 2.0,
		KG:                 b.KG,
		TransverseInertia:  BargeWaterplaneInertia(b.Beam, b.Length),
		WaterDensity:       DefaultWaterDensity,
	}
	return p, nil
}
