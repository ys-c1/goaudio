package audio

import (
	"fmt"
	"math"
	"text/tabwriter"
)

// Matrix 是 N 声道到两声道的下混矩阵：
//
//	L(t) = Σ_j Left[j]  * x_j(t)
//	R(t) = Σ_j Right[j] * x_j(t)
//
// 系数不采用任何固定标准配比，全部由实际素材的能量解出。
type Matrix struct {
	Left  []float64
	Right []float64
}

const (
	// angleMin 把声像角限制在开区间 (0, π/2) 内，
	// 保证每列两个系数都严格为正，任何声道都不会被结构性清零。
	angleMin = 0.02
	// rhoTolerance 是能量守恒与相关系数核验的数值容差。
	rhoTolerance = 1e-9
	// DefaultRhoLimit 是默认的相关系数容许上限（区间 [-ρ, ρ]）。
	DefaultRhoLimit = 0.9
)

// EnergyReport 记录一次下混的推导依据与核验结果。
type EnergyReport struct {
	Rate        int
	FrameCount  int
	AnglesDeg   []float64 // 每个声道解出的声像角（度）
	InputEnergy []float64 // 每个声道下混前的平均能量
	InputTotal  float64   // 下混前总能量
	OutputLeft  float64
	OutputRight float64
	OutputTotal float64 // 下混后总能量（左+右）
	EnergyRatio float64 // 输出总能量 / 输入总能量，守恒时为 1
	Gain        float64 // 全局能量守恒增益 g
	Correlation float64 // 下混后左右通道的皮尔逊相关系数
	RhoLimit    float64 // 容许的相关系数绝对值上限
	Matrix      Matrix
}

// SolveEnergyMatrix 按各声道实测能量推导下混矩阵。
//
// 推导（不照抄任何固定配比）：
//  1. 实测每声道能量 E_j = mean(x_j²)，取能量权重 w_j = E_j / max(E)；
//  2. 均衡排布角 φ_j = (π/2)·j/(N-1)（单声道取 π/4），声像角
//     θ_j = clamp(w_j·φ_j, angleMin, π/2-angleMin)，列向量 (cosθ_j, sinθ_j)
//     天然单位化——单声道单独发声时能量严格守恒，且两个系数恒正，
//     结构上不可能被完全抵消；
//  3. 用实际素材渲染一次，解全局增益 g = sqrt(E_入 / E_出)，
//     使下混前后总能量严格守恒（含声道间相关造成的功率起伏）；
//  4. 若相关系数超出 [-rhoLimit, rhoLimit]，逐级展宽排布角 s=1+0.25k
//     重新求解，直到两条核验同时通过；无可行解时返回相关系数核验错误。
func (m *MultiClip) SolveEnergyMatrix(rhoLimit float64) (Matrix, *EnergyReport, error) {
	if len(m.Channels) == 0 {
		return Matrix{}, nil, ErrNoChannels
	}
	if err := m.checkRates(); err != nil {
		return Matrix{}, nil, err
	}
	if rhoLimit <= 0 || rhoLimit > 1 {
		return Matrix{}, nil, errf("相关系数容许上限必须落在 (0,1]，得到 %v", rhoLimit)
	}

	n := m.nframes()
	nCh := len(m.Channels)
	energies := make([]float64, nCh)
	var eMax, eIn float64
	for j, c := range m.Channels {
		energies[j] = channelEnergy(c.Frames[:n])
		eIn += energies[j]
		if energies[j] > eMax {
			eMax = energies[j]
		}
	}
	weights := make([]float64, nCh)
	if eMax > 0 {
		for j := range weights {
			weights[j] = energies[j] / eMax
		}
	}

	var (
		best       Matrix
		bestAng    []float64
		bestRho    = math.Inf(1)
		bestGain   float64
		bestReport *EnergyReport
	)
	for k := 0; k <= 12; k++ {
		spread := 1 + 0.25*float64(k)
		angles := make([]float64, nCh)
		unit := Matrix{Left: make([]float64, nCh), Right: make([]float64, nCh)}
		for j := range angles {
			phi := math.Pi / 4
			if nCh > 1 {
				phi = math.Pi / 2 * float64(j) / float64(nCh-1)
			}
			theta := weights[j] * phi * spread
			theta = clamp(theta, angleMin, math.Pi/2-angleMin)
			angles[j] = theta
			unit.Left[j] = math.Cos(theta)
			unit.Right[j] = math.Sin(theta)
		}

		lRaw, rRaw := m.mixFloat(unit)
		eOut := meanEnergy(lRaw) + meanEnergy(rRaw)
		g := 1.0
		if eIn > 0 && eOut > 0 {
			g = math.Sqrt(eIn / eOut)
		}
		mat := scaleMatrix(unit, g)
		l, r := m.mixFloat(mat)
		rho := Correlation(l, r)

		if math.Abs(rho) < math.Abs(bestRho) {
			bestRho, best, bestAng, bestGain = rho, mat, angles, g
		}
		if math.Abs(rho) <= rhoLimit+rhoTolerance {
			rep := m.buildReport(mat, angles, energies, eIn, g, rho, rhoLimit, l, r)
			if err := VerifyNoCancellation(mat, m); err != nil {
				return Matrix{}, nil, err
			}
			return mat, rep, nil
		}
	}

	// 所有展宽级别都越界：返回最接近可行的矩阵，但按相关系数核验失败上报。
	l, r := m.mixFloat(best)
	bestReport = m.buildReport(best, bestAng, energies, eIn, bestGain, bestRho, rhoLimit, l, r)
	return best, bestReport, errf("%w：相关系数 %.4f，要求落在 [%.4f, %.4f]",
		ErrCorrelation, bestRho, -rhoLimit, rhoLimit)
}

// DownmixMatrix 用给定矩阵执行下混。应用前分别独立执行：
//   - 矩阵合法性核验（含“系数全负”报错，指名违规声道）；
//   - 单独发声声道的抵消核验；
//   - 左右通道相关系数区间核验。
func (m *MultiClip) DownmixMatrix(mat Matrix, rhoLimit float64) (*StereoClip, *EnergyReport, error) {
	if len(m.Channels) == 0 {
		return nil, nil, ErrNoChannels
	}
	if err := m.checkRates(); err != nil {
		return nil, nil, err
	}
	if err := ValidateMatrix(mat, len(m.Channels)); err != nil {
		return nil, nil, err
	}
	if rhoLimit <= 0 || rhoLimit > 1 {
		return nil, nil, errf("相关系数容许上限必须落在 (0,1]，得到 %v", rhoLimit)
	}

	// 两条核验各自独立调用，互不吞并对方的错误。
	if err := VerifyNoCancellation(mat, m); err != nil {
		return nil, nil, err
	}
	l, r := m.mixFloat(mat)
	rho := Correlation(l, r)
	if err := VerifyCorrelation(l, r, -rhoLimit, rhoLimit); err != nil {
		return nil, nil, err
	}

	n := m.nframes()
	out := &StereoClip{Rate: m.Channels[0].Rate, Left: make([]int16, n), Right: make([]int16, n)}
	for t := 0; t < n; t++ {
		out.Left[t] = saturateInt16(int32(math.Round(l[t])))
		out.Right[t] = saturateInt16(int32(math.Round(r[t])))
	}

	energies := make([]float64, len(m.Channels))
	var eIn float64
	for j, c := range m.Channels {
		energies[j] = channelEnergy(c.Frames[:n])
		eIn += energies[j]
	}
	rep := m.buildReport(mat, nil, energies, eIn, 0, rho, rhoLimit, l, r)
	return out, rep, nil
}

// ValidateMatrix 核验矩阵尺寸与系数合法性。
func ValidateMatrix(mat Matrix, nChannels int) error {
	if nChannels == 0 {
		return ErrNoChannels
	}
	if len(mat.Left) != nChannels || len(mat.Right) != nChannels {
		return errf("矩阵尺寸不匹配：需要 2×%d，得到 2×%d/2×%d",
			nChannels, len(mat.Left), len(mat.Right))
	}
	allNeg := true
	for j := 0; j < nChannels; j++ {
		a, b := mat.Left[j], mat.Right[j]
		if math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
			return errf("矩阵系数非法：第 %d 声道含 NaN/Inf 系数", j+1)
		}
		if a >= 0 || b >= 0 {
			allNeg = false
		}
	}
	if allNeg {
		return errf("%w：第 1 至 %d 声道的左右系数全部小于零，整体反相不可用",
			ErrAllNegative, nChannels)
	}
	return nil
}

// VerifyCorrelation 独立核验左右通道相关系数是否落在 [lo, hi]。
// 任一通道方差为零时相关系数无定义，按 0 处理（不构成越界）。
func VerifyCorrelation(left, right []float64, lo, hi float64) error {
	rho := Correlation(left, right)
	if rho < lo-rhoTolerance || rho > hi+rhoTolerance {
		return errf("%w：相关系数 %.4f，要求落在 [%.4f, %.4f]", ErrCorrelation, rho, lo, hi)
	}
	return nil
}

// VerifyNoCancellation 独立核验：每个带能量的声道单独发声时，
// 经自己的矩阵列渲染后不得被完全抵消（残余能量必须严格为正）。
func VerifyNoCancellation(mat Matrix, m *MultiClip) error {
	n := m.nframes()
	for j, c := range m.Channels {
		eIn := channelEnergy(c.Frames[:n])
		if eIn <= 0 {
			continue
		}
		eSolo := (mat.Left[j]*mat.Left[j] + mat.Right[j]*mat.Right[j]) * eIn
		if eSolo <= rhoTolerance {
			return errf("%w：第 %d 声道单独发声时输出能量为 %g（输入能量 %g）",
				ErrCancellation, j+1, eSolo, eIn)
		}
	}
	return nil
}

// Correlation 返回两信号的皮尔逊相关系数；方差为零时返回 0。
func Correlation(left, right []float64) float64 {
	if len(left) != len(right) || len(left) == 0 {
		return 0
	}
	var sl, sr float64
	for i := range left {
		sl += left[i]
		sr += right[i]
	}
	n := float64(len(left))
	ml, mr := sl/n, sr/n
	var cov, vl, vr float64
	for i := range left {
		dl, dr := left[i]-ml, right[i]-mr
		cov += dl * dr
		vl += dl * dl
		vr += dr * dr
	}
	if vl <= rhoTolerance || vr <= rhoTolerance {
		return 0
	}
	return cov / math.Sqrt(vl*vr)
}

func (m *MultiClip) mixFloat(mat Matrix) (left, right []float64) {
	n := m.nframes()
	left = make([]float64, n)
	right = make([]float64, n)
	for j, c := range m.Channels {
		a, b := mat.Left[j], mat.Right[j]
		for t := 0; t < n; t++ {
			x := float64(c.Frames[t])
			left[t] += a * x
			right[t] += b * x
		}
	}
	return left, right
}

func (m *MultiClip) buildReport(mat Matrix, angles, energies []float64, eIn, g, rho, rhoLimit float64, l, r []float64) *EnergyReport {
	eL, eR := meanEnergy(l), meanEnergy(r)
	rep := &EnergyReport{
		Rate:        m.Channels[0].Rate,
		FrameCount:  m.nframes(),
		AnglesDeg:   append([]float64(nil), angles...),
		InputEnergy: append([]float64(nil), energies...),
		InputTotal:  eIn,
		OutputLeft:  eL,
		OutputRight: eR,
		OutputTotal: eL + eR,
		Gain:        g,
		Correlation: rho,
		RhoLimit:    rhoLimit,
		Matrix:      mat,
	}
	if eIn > 0 {
		rep.EnergyRatio = rep.OutputTotal / eIn
	}
	return rep
}

func channelEnergy(x []int16) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		f := float64(v)
		s += f * f
	}
	return s / float64(len(x))
}

func meanEnergy(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += v * v
	}
	return s / float64(len(x))
}

func scaleMatrix(mat Matrix, g float64) Matrix {
	out := Matrix{Left: make([]float64, len(mat.Left)), Right: make([]float64, len(mat.Right))}
	for j := range mat.Left {
		out.Left[j] = mat.Left[j] * g
		out.Right[j] = mat.Right[j] * g
	}
	return out
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func errf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// String 输出矩阵系数与下混前后能量对照表。
func (r *EnergyReport) String() string {
	var buf []byte
	w := tabwriter.NewWriter(&sliceWriter{buf: &buf}, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "下混矩阵（%d 声道 → 2，采样率 %d Hz，帧数 %d，增益 g=%.6f）\n",
		len(r.Matrix.Left), r.Rate, r.FrameCount, r.Gain)
	fmt.Fprintln(w, "声道\t输入能量\t声像角(°)\t左系数\t右系数")
	for j := range r.Matrix.Left {
		angle := math.NaN()
		if j < len(r.AnglesDeg) {
			angle = r.AnglesDeg[j] * 180 / math.Pi
		}
		fmt.Fprintf(w, "%d\t%.4f\t%.2f\t%.6f\t%.6f\n",
			j+1, r.InputEnergy[j], angle, r.Matrix.Left[j], r.Matrix.Right[j])
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "能量对照\t总能量\t占比\n")
	fmt.Fprintf(w, "下混前\t%.4f\t100.00%%\n", r.InputTotal)
	fmt.Fprintf(w, "下混后-左\t%.4f\t%.2f%%\n", r.OutputLeft, pct(r.OutputLeft, r.InputTotal))
	fmt.Fprintf(w, "下混后-右\t%.4f\t%.2f%%\n", r.OutputRight, pct(r.OutputRight, r.InputTotal))
	fmt.Fprintf(w, "下混后-合计\t%.4f\t%.2f%%（比值 %.6f）\n",
		r.OutputTotal, pct(r.OutputTotal, r.InputTotal), r.EnergyRatio)
	fmt.Fprintf(w, "左右相关系数\t%.6f（容许区间 [%.4f, %.4f]）\n",
		r.Correlation, -r.RhoLimit, r.RhoLimit)
	w.Flush()
	return string(buf)
}

func pct(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return 100 * part / total
}

// sliceWriter 适配 tabwriter，避免引入 bytes 之外的额外封装。
type sliceWriter struct{ buf *[]byte }

func (s *sliceWriter) Write(p []byte) (int, error) {
	*s.buf = append(*s.buf, p...)
	return len(p), nil
}
