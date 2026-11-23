package audio

import (
	"fmt"
	"math"
	"sort"
)

// SlopeLimits 是拐点两侧斜率允许落入的闭区间（dB/dB）。
type SlopeLimits struct {
	Min float64
	Max float64
}

// KneeFit 是从实测输入/输出电平对应反解出的软拐点。
//
// PositionDB 为拐点中心（即门限）位置，Curvature 为拐点区的二阶导数
// d²y/dx²（1/dB，压缩时为负），KneeDB 为由曲率换算出的拐点总宽度。
// LeftSlope/RightSlope 为实测数据在拐点两侧回归出的斜率，
// RMSE 为模型对实测压缩量的拟合均方根误差（dB）。
type KneeFit struct {
	PositionDB float64
	Curvature  float64
	KneeDB     float64
	Ratio      float64
	LeftSlope  float64
	RightSlope float64
	RMSE       float64
}

// VerifySlopes 核验拐点两侧实测斜率是否都落在 lim 内。
func (f KneeFit) VerifySlopes(lim SlopeLimits) error {
	if lim.Min > lim.Max {
		return fmt.Errorf("%w: 区间上下界颠倒 [%g, %g]", ErrSlope, lim.Min, lim.Max)
	}
	if f.LeftSlope < lim.Min || f.LeftSlope > lim.Max {
		return fmt.Errorf("%w: 左侧 %.4f 不在 [%g, %g]", ErrSlope, f.LeftSlope, lim.Min, lim.Max)
	}
	if f.RightSlope < lim.Min || f.RightSlope > lim.Max {
		return fmt.Errorf("%w: 右侧 %.4f 不在 [%g, %g]", ErrSlope, f.RightSlope, lim.Min, lim.Max)
	}
	return nil
}

// reductionOf 返回一对电平对应的压缩量（dB，非负）。
func reductionOf(p LevelPair) float64 { return p.InputDB - p.OutputDB }

// FitSoftKnee 从实测的输入/输出电平对应反解软拐点位置与曲率。
//
// 模型为 C1 连续的二次拐点：拐点区 [T-W/2, T+W/2] 内压缩量按抛物线
// 过渡，区外为两条线性段，两侧斜率分别为 1 与 1/R（R 为压缩比）。
// 三个未知量 (T, W, R) 一起做确定性的粗网格搜索加坐标下降精修，
// 不使用随机数，因而同一组实测数据必然得到相同结果。
//
// 覆盖率必须同时包含门限两侧的线性段，否则 W 与 R 无法分离；
// 只有拐点区内数据时返回 ErrIdentify。
func FitSoftKnee(pairs []LevelPair) (KneeFit, error) {
	if len(pairs) < 3 {
		return KneeFit{}, ErrLevels
	}
	pts := make([]LevelPair, len(pairs))
	copy(pts, pairs)
	sort.Slice(pts, func(i, j int) bool { return pts[i].InputDB < pts[j].InputDB })

	minIn, maxIn := pts[0].InputDB, pts[len(pts)-1].InputDB
	span := maxIn - minIn
	if span <= 1e-9 {
		return KneeFit{}, &ConfigError{Field: "levels", Value: span, Err: ErrIdentify}
	}

	// 必须先确认素材里确实存在压缩（否则任何 W 都能拟合零压缩）。
	maxRed := 0.0
	for _, p := range pts {
		if r := reductionOf(p); r > maxRed {
			maxRed = r
		}
	}
	if maxRed <= 1e-6 {
		return KneeFit{}, fmt.Errorf("%w: 实测压缩量恒为零", ErrIdentify)
	}

	sse := func(T, W, R float64) float64 {
		s := 0.0
		for _, p := range pts {
			g := SoftKneeGainDB(p.InputDB, T, R, W)
			d := g - (p.OutputDB - p.InputDB)
			s += d * d
		}
		return s
	}

	// 粗网格确定盆地，避免坐标下降落入错误局部极小。
	const grid = 32
	bestT, bestW, bestR := minIn, span/2, 4.0
	best := math.Inf(1)
	for i := 0; i <= grid; i++ {
		T := minIn + span*float64(i)/grid
		for j := 0; j <= grid; j++ {
			W := 0.05 + (3*span-0.05)*float64(j)/grid
			for k := 0; k <= grid; k++ {
				R := 1.05 * math.Pow(64/1.05, float64(k)/grid)
				if v := sse(T, W, R); v < best {
					best, bestT, bestW, bestR = v, T, W, R
				}
			}
		}
	}

	// 坐标下降精修，步长逐轮减半。
	stepT := span / float64(grid)
	stepW := 3 * span / float64(grid)
	stepR := bestR * 0.1
	limit := func(T, W, R float64) (float64, float64, float64) {
		T = clampDB(T, minIn-span, maxIn+span)
		if W < 1e-4 {
			W = 1e-4
		}
		if R < 1.0001 {
			R = 1.0001
		}
		if R > 1000 {
			R = 1000
		}
		return T, W, R
	}
	for iter := 0; iter < 200; iter++ {
		moved := false
		for _, d := range []float64{-1, 1} {
			if T, W, R := limit(bestT+d*stepT, bestW, bestR); sse(T, W, R) < best {
				best, bestT, bestW, bestR = sse(T, W, R), T, W, R
				moved = true
			}
			if T, W, R := limit(bestT, bestW+d*stepW, bestR); sse(T, W, R) < best {
				best, bestT, bestW, bestR = sse(T, W, R), T, W, R
				moved = true
			}
			if T, W, R := limit(bestT, bestW, bestR+d*stepR); sse(T, W, R) < best {
				best, bestT, bestW, bestR = sse(T, W, R), T, W, R
				moved = true
			}
		}
		if !moved {
			stepT /= 2
			stepW /= 2
			stepR /= 2
			if stepT < 1e-9 && stepW < 1e-9 && stepR < 1e-9 {
				break
			}
		}
	}

	fit := KneeFit{
		PositionDB: bestT,
		KneeDB:     bestW,
		Ratio:      bestR,
		Curvature:  (1/bestR - 1) / bestW,
		RMSE:       math.Sqrt(best / float64(len(pts))),
	}
	left, right, err := measuredSlopes(pts, bestT, bestW)
	if err != nil {
		return KneeFit{}, err
	}
	fit.LeftSlope, fit.RightSlope = left, right
	return fit, nil
}

// FitSoftKneeRatio 在压缩比已知时反解拐点位置与曲率，只以 (T, W)
// 为未知量；压缩比未知时请用 FitSoftKnee。
func FitSoftKneeRatio(pairs []LevelPair, ratio float64) (KneeFit, error) {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 1 {
		return KneeFit{}, &ConfigError{Field: "ratio", Value: ratio, Err: ErrRatio}
	}
	if len(pairs) < 3 {
		return KneeFit{}, ErrLevels
	}
	pts := make([]LevelPair, len(pairs))
	copy(pts, pairs)
	sort.Slice(pts, func(i, j int) bool { return pts[i].InputDB < pts[j].InputDB })
	minIn, maxIn := pts[0].InputDB, pts[len(pts)-1].InputDB
	span := maxIn - minIn
	if span <= 1e-9 {
		return KneeFit{}, &ConfigError{Field: "levels", Value: span, Err: ErrIdentify}
	}
	maxRed := 0.0
	for _, p := range pts {
		if r := reductionOf(p); r > maxRed {
			maxRed = r
		}
	}
	if maxRed <= 1e-6 {
		return KneeFit{}, fmt.Errorf("%w: 实测压缩量恒为零", ErrIdentify)
	}
	sse := func(T, W float64) float64 {
		s := 0.0
		for _, p := range pts {
			g := SoftKneeGainDB(p.InputDB, T, ratio, W)
			d := g - (p.OutputDB - p.InputDB)
			s += d * d
		}
		return s
	}
	const grid = 40
	bestT, bestW := minIn, span/2
	best := math.Inf(1)
	for i := 0; i <= grid; i++ {
		T := minIn + span*float64(i)/grid
		for j := 0; j <= grid; j++ {
			W := 0.05 + (3*span-0.05)*float64(j)/grid
			if v := sse(T, W); v < best {
				best, bestT, bestW = v, T, W
			}
		}
	}
	stepT := span / float64(grid)
	stepW := 3 * span / float64(grid)
	for iter := 0; iter < 200; iter++ {
		moved := false
		for _, d := range []float64{-1, 1} {
			T := clampDB(bestT+d*stepT, minIn-span, maxIn+span)
			if v := sse(T, bestW); v < best {
				best, bestT, moved = v, T, true
			}
			W := bestW + d*stepW
			if W < 1e-4 {
				W = 1e-4
			}
			if v := sse(bestT, W); v < best {
				best, bestW, moved = v, W, true
			}
		}
		if !moved {
			stepT /= 2
			stepW /= 2
			if stepT < 1e-9 && stepW < 1e-9 {
				break
			}
		}
	}
	fit := KneeFit{
		PositionDB: bestT,
		KneeDB:     bestW,
		Ratio:      ratio,
		Curvature:  (1/ratio - 1) / bestW,
		RMSE:       math.Sqrt(best / float64(len(pts))),
	}
	left, right, err := measuredSlopes(pts, bestT, bestW)
	if err != nil {
		return KneeFit{}, err
	}
	if math.Abs(left-1) > 0.05 {
		return KneeFit{}, fmt.Errorf("%w: 拐点前斜率实测 %.3f，应约为 1", ErrIdentify, left)
	}
	fit.LeftSlope, fit.RightSlope = left, right
	return fit, nil
}

// measuredSlopes 在拐点区之外的实测点上回归出两侧斜率。
func measuredSlopes(pts []LevelPair, T, W float64) (left, right float64, err error) {
	lo, hi := T-W/2, T+W/2
	var lp, rp []LevelPair
	for _, p := range pts {
		switch {
		case p.InputDB <= lo:
			lp = append(lp, p)
		case p.InputDB >= hi:
			rp = append(rp, p)
		}
	}
	if len(lp) < 2 || len(rp) < 2 {
		return 0, 0, fmt.Errorf("%w: 左侧 %d 点，右侧 %d 点", ErrSlopeData, len(lp), len(rp))
	}
	return regressSlope(lp), regressSlope(rp), nil
}

func regressSlope(pts []LevelPair) float64 {
	var sx, sy float64
	for _, p := range pts {
		sx += p.InputDB
		sy += p.OutputDB
	}
	n := float64(len(pts))
	mx, my := sx/n, sy/n
	var num, den float64
	for _, p := range pts {
		dx := p.InputDB - mx
		num += dx * (p.OutputDB - my)
		den += dx * dx
	}
	if den == 0 {
		return 1
	}
	return num / den
}

func clampDB(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// KneeInputForReduction 由目标压缩量反解对应的输入电平（拐点位置）。
//
// 解的是 C1 软拐点曲线的反函数，因此结果在拐点边界处同样连续：
//
//	目标 ≤ 拐点区最大压缩量: x = T - W/2 + sqrt(2W·g/(1-1/R))
//	否则:                   x = T + g/(1-1/R)
//
// 其中 g 为目标压缩量（dB，非负）。
func KneeInputForReduction(targetDB, thresholdDB, ratio, kneeDB float64) (float64, error) {
	if math.IsNaN(targetDB) || math.IsInf(targetDB, 0) || targetDB < 0 {
		return 0, &ConfigError{Field: "reduction", Value: targetDB, Err: ErrReduction}
	}
	if math.IsNaN(thresholdDB) || math.IsInf(thresholdDB, 0) || thresholdDB >= 0 {
		return 0, &ConfigError{Field: "threshold", Value: thresholdDB, Err: ErrThreshold}
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 1 {
		return 0, &ConfigError{Field: "ratio", Value: ratio, Err: ErrRatio}
	}
	if math.IsNaN(kneeDB) || math.IsInf(kneeDB, 0) || kneeDB < 0 {
		return 0, &ConfigError{Field: "knee", Value: kneeDB, Err: ErrKnee}
	}
	a := 1/ratio - 1 // 负
	if kneeDB == 0 {
		// 硬拐点：门限以下无压缩，目标压缩量只能在门限以上取得。
		if targetDB == 0 {
			return thresholdDB, nil
		}
		return thresholdDB + targetDB/(-a), nil
	}
	maxKnee := -a * kneeDB / 2
	if targetDB <= maxKnee {
		return thresholdDB - kneeDB/2 + math.Sqrt(2*kneeDB*targetDB/(-a)), nil
	}
	return thresholdDB + targetDB/(-a), nil
}

// MeasuredKneeInput 在实测对应表上按压缩量线性插值出输入电平。
// 目标压缩量必须落在实测覆盖范围内，否则返回 ErrReduction。
func MeasuredKneeInput(pairs []LevelPair, targetDB float64) (float64, error) {
	if math.IsNaN(targetDB) || math.IsInf(targetDB, 0) || targetDB < 0 {
		return 0, &ConfigError{Field: "reduction", Value: targetDB, Err: ErrReduction}
	}
	if len(pairs) < 2 {
		return 0, ErrLevels
	}
	pts := make([]LevelPair, len(pairs))
	copy(pts, pairs)
	sort.Slice(pts, func(i, j int) bool { return reductionOf(pts[i]) < reductionOf(pts[j]) })
	first, last := reductionOf(pts[0]), reductionOf(pts[len(pts)-1])
	if targetDB < first || targetDB > last {
		return 0, fmt.Errorf("%w: 目标 %.3f dB 超出实测 [%.3f, %.3f]", ErrReduction, targetDB, first, last)
	}
	for i := 1; i < len(pts); i++ {
		r0, r1 := reductionOf(pts[i-1]), reductionOf(pts[i])
		if targetDB <= r1 && r1 > r0 {
			t := (targetDB - r0) / (r1 - r0)
			return pts[i-1].InputDB + t*(pts[i].InputDB-pts[i-1].InputDB), nil
		}
	}
	return pts[len(pts)-1].InputDB, nil
}

// KneeInverseErrorBoundDB 是"由目标压缩量反解拐点位置"允许的实测—
// 反解差值上界（dB）。
//
// 前提：实测对应表以 0.5 dB 步进测量，实测位置由压缩量在该表上线性
// 插值取得。该前提下实测—反解差值实测最大约 0.013 dB；上界取 0.05 dB，
// 约四倍余量。表更粗时插值误差会增大，需相应放宽。
const KneeInverseErrorBoundDB = 0.05
