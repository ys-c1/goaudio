package audio

import (
	"fmt"
	"math"
)

// RepairError 携带拒绝/验证失败的发生位置（帧索引；全局校验时为 -1）。
type RepairError struct {
	Err   error
	Index int // 削平段起始帧索引；全局或配置类错误为 -1
	End   int // 削平段结束帧索引（含）；不适用时为 -1
}

func (e *RepairError) Error() string {
	if e.Index >= 0 {
		return fmt.Sprintf("%v（位置：第 %d-%d 帧，起于 %d）", e.Err, e.Index, e.End, e.Index)
	}
	return e.Err.Error()
}

func (e *RepairError) Unwrap() error { return e.Err }

// RepairConfig 控制削波修复。零值字段会在 DefaultRepairConfig 基础上填充。
type RepairConfig struct {
	// ClipLevel 为削波判定阈值的绝对值。默认满幅 32767；
	// 实际录音在更低电平硬削（如 30000）时显式给出。
	ClipLevel int
	// FullScale 满幅绝对值，默认 32767（int16 正满幅；投影同时覆盖 -32768）。
	FullScale int
	// MaxFlat 单个削平段允许的最大长度（样本数），超过即拒绝。默认 64。
	MaxFlat int
	// MaxOrder 线性预测最高阶数，默认 12。
	MaxOrder int
	// Window 自相关定阶时每侧使用的动态样本数，默认 128。
	Window int
	// JumpTol 两侧一阶差分连续性容差（int16 单位），默认 2。
	JumpTol float64
}

// DefaultRepairConfig 返回默认配置。
func DefaultRepairConfig() RepairConfig {
	return RepairConfig{
		ClipLevel: 32767,
		FullScale: 32767,
		MaxFlat:   64,
		MaxOrder:  12,
		Window:    128,
		JumpTol:   2.0,
	}
}

// SegmentReport 单个削平段的修复记录。
type SegmentReport struct {
	Start        int     // 起始帧（含）
	End          int     // 结束帧（含）
	Length       int     // 削平段长度 = 修复样本数
	Positive     bool    // 正向削波（true）或负向（false）
	Order        int     // 残差自相关定出的预测阶数
	ResidualRMSE float64 // 该阶数下邻域动态样本的前向预测残差 RMSE
	Changed      int     // 投影/量化后实际发生变化的样本数
}

// RepairReport 整段音频的修复汇总。
type RepairReport struct {
	Segments      []SegmentReport
	RepairedCount int // 修复样本总数（全部削平段长度之和）
	PeakAfter     int // 修复后 int16 峰值绝对值
}

// lsRow 一条预测方程：coef 为各变量（按全局帧索引）系数，rhs 为常数侧。
// 方程含义 Σ coef[k]·x[k] = rhs。
type lsRow struct {
	coef map[int]float64
	rhs  float64
}

// Declip 检测并修复削平段。不修改入参，返回修复后的新 Clip 与逐段报告。
//
// 修复流程：
//  1. 检测连续贴在同一削波电平上的平台段（DetectFlatRuns）；
//  2. 每段收集两侧动态样本（贴削波电平的样本一律排除），用残差自相关 +
//     Levinson 解 Yule-Walker 定预测阶数；
//  3. 把整段平台当作缺失样本，前向/后向线性预测联合最小二乘解出插值；
//  4. 按已知动态范围（削波电平到满幅）做活动集投影，再量化回 int16；
//  5. 两个独立验证器分别检查满幅峰值与两侧一阶差分连续性。
func Declip(c *Clip, cfg RepairConfig) (*Clip, *RepairReport, error) {
	if c == nil || c.Rate <= 0 {
		return nil, nil, &RepairError{Err: ErrSampleRate, Index: -1, End: -1}
	}
	d := DefaultRepairConfig()
	if cfg.ClipLevel > 0 {
		d.ClipLevel = cfg.ClipLevel
	}
	if cfg.FullScale > 0 {
		d.FullScale = cfg.FullScale
	}
	if cfg.MaxFlat > 0 {
		d.MaxFlat = cfg.MaxFlat
	}
	if cfg.MaxOrder > 0 {
		d.MaxOrder = cfg.MaxOrder
	}
	if cfg.Window > 0 {
		d.Window = cfg.Window
	}
	if cfg.JumpTol > 0 {
		d.JumpTol = cfg.JumpTol
	}
	if d.ClipLevel > d.FullScale || d.MaxFlat <= 0 || d.MaxOrder <= 0 {
		return nil, nil, &RepairError{Err: ErrConfig, Index: -1, End: -1}
	}

	runs := DetectFlatRuns(c.Frames, d.ClipLevel)
	out := &Clip{Rate: c.Rate, Frames: append([]int16(nil), c.Frames...)}
	report := &RepairReport{}

	// float 工作副本，供独立验证器在量化前检查。
	work := make([]float64, len(c.Frames))
	for i, v := range c.Frames {
		work[i] = float64(v)
	}

	for _, r := range runs {
		s, e := r[0], r[1]
		if s == 0 || e == len(c.Frames)-1 {
			return nil, nil, &RepairError{Err: ErrEdgeClip, Index: s, End: e}
		}
		if e-s+1 > d.MaxFlat {
			return nil, nil, &RepairError{Err: ErrFlatLimit, Index: s, End: e}
		}
		positive := c.Frames[s] > 0

		dyn := dynamicIndices(work, d.ClipLevel, s, e, d.Window)
		// LP 在零均值信号上估计：用动态样本均值代表局部直流分量。
		mu := meanOf(work, dyn)
		order, rmse, err := estimateOrder(work, dyn, mu, d.MaxOrder)
		if err != nil {
			return nil, nil, &RepairError{Err: err, Index: s, End: e}
		}

		coef, _ := levinsonCoefs(work, dyn, mu, order)
		// 缺口求解在零均值空间进行，解出后加回均值；投影边界也平移到零均值空间。
		sol0 := solveGapCentered(work, s, e, coef, mu)
		low, high := gapBounds(positive, d.ClipLevel, d.FullScale)
		sol0 = projectActiveSetCentered(work, s, e, coef, mu, sol0, low-mu, high-mu)

		seg := SegmentReport{
			Start: s, End: e, Length: e - s + 1,
			Positive: positive, Order: order, ResidualRMSE: rmse,
		}
		for i := s; i <= e; i++ {
			q := clampInt16(math.Round(sol0[i-s] + mu))
			work[i] = float64(q)
			out.Frames[i] = q
			if q != c.Frames[i] {
				seg.Changed++
			}
		}
		report.Segments = append(report.Segments, seg)
		report.RepairedCount += seg.Length
	}

	// 验证器 A：满幅峰值（先查 float 重建值，再查量化后的 int16）。
	if err := VerifyPeakFloat(work, d.FullScale); err != nil {
		return nil, nil, err
	}
	if p := Peak(out); p > d.FullScale {
		return nil, nil, &RepairError{Err: ErrPeakOverflow, Index: -1, End: -1}
	}
	// 验证器 B：每个削平段两侧一阶差分连续性。
	for _, r := range runs {
		if err := VerifyBoundaryContinuous(work, r[0], r[1], d.ClipLevel, d.JumpTol); err != nil {
			return nil, nil, err
		}
	}

	report.PeakAfter = Peak(out)
	return out, report, nil
}

// DetectFlatRuns 返回所有削平段：连续两个及以上样本完全相等且绝对值达到阈值。
// 每段为 {起始, 结束} 索引对。
func DetectFlatRuns(frames []int16, clipLevel int) [][2]int {
	var runs [][2]int
	n := len(frames)
	i := 0
	for i < n {
		v := frames[i]
		if int(abs16(v)) < clipLevel {
			i++
			continue
		}
		j := i + 1
		for j < n && frames[j] == v {
			j++
		}
		if j-i >= 2 {
			runs = append(runs, [2]int{i, j - 1})
		}
		i = j
	}
	return runs
}

// dynamicIndices 收集缺口 [s,e] 两侧各至多 window 个动态样本的全局索引。
// 贴在削波电平上的样本（任何平台的一部分）全部排除；返回索引升序排列。
func dynamicIndices(x []float64, clipLevel, s, e, window int) []int {
	level := float64(clipLevel)
	var dyn []int
	for k := s - 1; k >= 0 && len(dyn) < window; k-- {
		if math.Abs(x[k]) < level {
			dyn = append(dyn, k)
		}
	}
	// 左侧是逆序收集的，反转回来。
	for i, j := 0, len(dyn)-1; i < j; i, j = i+1, j-1 {
		dyn[i], dyn[j] = dyn[j], dyn[i]
	}
	right := 0
	for k := e + 1; k < len(x) && right < window; k++ {
		if math.Abs(x[k]) < level {
			dyn = append(dyn, k)
			right++
		}
	}
	return dyn
}

// dynamicRuns 把动态索引切成连续段（相邻索引差 1），自相关与残差不跨段。
func dynamicRuns(dyn []int) [][]int {
	var runs [][]int
	cur := []int{dyn[0]}
	for _, k := range dyn[1:] {
		if k == cur[len(cur)-1]+1 {
			cur = append(cur, k)
		} else {
			runs = append(runs, cur)
			cur = []int{k}
		}
	}
	runs = append(runs, cur)
	return runs
}

// estimateOrder 用动态样本的残差自相关确定 LP 阶数。
//
// 为什么这样定阶：对候选阶数 p=1..maxP 解 Yule-Walker（Levinson 递推），
// 得到前向预测残差 e[k]=x[k]-Σ a_j x[k-j]。真实 AR(p) 过程在正确阶数下
// 残差白化——其自相关在所有非零滞后上应落入白噪声带 ±1.96/√N。
// 因此优先取“残差通过白化检验的最小 p”：更小的 p 残差仍含可预测结构
// （欠拟合），更大的 p 只是在拟合噪声（过拟合、矩阵病态）。
//
// 有限窗内的周期信号（如录音中的乐音）残差可能在所有候选阶数下都保留
// 弱结构，白化检验无一通过。此时退而用残差自相关驱动的 AIC 兜底：
// AIC(p)=N·ln(σ²_p)+2p，σ²_p 即残差方差，随 p 增大单调下降、2p 惩罚
// 过拟合，取 AIC 最小的阶数。两条规则都建立在残差自相关给出的预测
// 误差之上。只有动态样本恒定（无任何动态）或有效残差样本不足时，
// 阶数才为零，并按要求拒绝修复。
func estimateOrder(x []float64, dyn []int, mu float64, maxP int) (int, float64, error) {
	if len(dyn) <= maxP+2 {
		return 0, 0, ErrZeroOrder
	}
	energy := 0.0
	for _, k := range dyn {
		d := x[k] - mu
		energy += d * d
	}
	if energy < 1e-9 {
		// 动态样本恒定：没有任何动态可用于预测，阶数只能为零。
		return 0, 0, ErrZeroOrder
	}

	type cand struct {
		p     int
		rmse  float64
		white bool
		aic   float64
	}
	var cands []cand
	limit := stableOrder(x, dyn, mu, maxP)
	if limit < 1 {
		return 0, 0, ErrZeroOrder
	}
	for p := 1; p <= limit; p++ {
		a, ok := levinsonCoefs(x, dyn, mu, p)
		if !ok {
			continue
		}
		res := forwardResiduals(x, dyn, mu, a)
		n := len(res)
		if n < 4 {
			continue
		}
		var sq float64
		for _, v := range res {
			sq += v * v
		}
		rmse := math.Sqrt(sq / float64(n))
		white := residualIsWhite(res, p)
		variance := sq / float64(n)
		if variance < 1e-12 {
			variance = 1e-12
		}
		aic := float64(n)*math.Log(variance) + 2*float64(p)
		cands = append(cands, cand{p, rmse, white, aic})
	}
	if len(cands) == 0 {
		return 0, 0, ErrZeroOrder
	}
	// 第一选择：通过白化检验的最小阶。
	for _, cd := range cands {
		if cd.white {
			return cd.p, cd.rmse, nil
		}
	}
	// 兜底：AIC 最小的阶（残差自相关信息准则）。
	best := cands[0]
	for _, cd := range cands[1:] {
		if cd.aic < best.aic {
			best = cd
		}
	}
	return best.p, best.rmse, nil
}

// meanOf 返回动态样本的全局均值（局部直流分量估计）。
func meanOf(x []float64, dyn []int) float64 {
	var sum float64
	for _, k := range dyn {
		sum += x[k]
	}
	return sum / float64(len(dyn))
}

// forwardResiduals 在每个连续动态段内计算零均值前向预测残差，不跨越段边界。
func forwardResiduals(x []float64, dyn []int, mu float64, a []float64) []float64 {
	p := len(a)
	var res []float64
	for _, run := range dynamicRuns(dyn) {
		if len(run) <= p {
			continue
		}
		for idx := p; idx < len(run); idx++ {
			k := run[idx]
			pred := mu
			for j := 1; j <= p; j++ {
				pred += a[j-1] * (x[run[idx-j]] - mu)
			}
			res = append(res, x[k]-pred)
		}
	}
	return res
}

// residualIsWhite 用 95% 白噪声带检验残差自相关（端口检验）。
func residualIsWhite(res []float64, p int) bool {
	n := len(res)
	if n < 8 {
		return false
	}
	var c0 float64
	for _, v := range res {
		c0 += v * v
	}
	if c0 < 1e-12 {
		return true // 残差全零：完美白化。
	}
	band := 1.96 / math.Sqrt(float64(n))
	maxLag := p
	if maxLag > n/4 {
		maxLag = n / 4
	}
	if maxLag < 1 {
		maxLag = 1
	}
	for lag := 1; lag <= maxLag; lag++ {
		var cl float64
		for k := lag; k < n; k++ {
			cl += res[k] * res[k-lag]
		}
		if math.Abs(cl/c0) > band {
			return false
		}
	}
	return true
}

// levinsonCoefs 在全局零均值信号 y=x-mu 上估计 p 阶线性预测系数
// a[0..p-1]（y[k] ≈ Σ a_j y[k-j-1]），采用 Burg 格型法。
//
// 为什么不用自相关/Levinson：削波把信号切成多个短动态段，且动态样本系统性
// 排除了波形顶部/底部，自相关估计存在选择性偏差（实测 r1 从理论 0.999 偏到
// 0.975），导致谐波阶系数失真、插值塌回削波电平。Burg 法直接在可用样本上
// 最小化前向与后向预测误差之和，逐阶求反射系数 k_m，不经过有偏自相关，对短
// 数据/缺失数据稳健，并由构造保证 |k_m|<1（最小相位、系统稳定）。
//
// 多个动态段的前/后向误差能量跨段池化后再求 k，等价于在同一平稳过程的所有
// 可用观测上联合拟合。无足够支撑（没有任何段长度 > p+1）时 ok=false。
func levinsonCoefs(x []float64, dyn []int, mu float64, p int) (coef []float64, ok bool) {
	var runs [][]float64
	for _, run := range dynamicRuns(dyn) {
		if len(run) <= p+1 {
			continue
		}
		yv := make([]float64, len(run))
		for i, k := range run {
			yv[i] = x[k] - mu
		}
		runs = append(runs, yv)
	}
	if len(runs) == 0 {
		return make([]float64, p), false
	}

	a := make([]float64, p)
	alpha := make([]float64, 0, p) // 误差滤波器系数（Burg/Levinson 标准约定）
	effOrder := 0
	// 每段独立维护前/后向预测误差与初始误差能量。
	type burg struct {
		y              []float64
		f, b           []float64
		eFwd, eBak, e0 float64
	}
	states := make([]burg, len(runs))
	for i, yv := range runs {
		n := len(yv)
		f := append([]float64(nil), yv...)
		b := append([]float64(nil), yv...)
		var e0 float64
		for _, v := range yv {
			e0 += v * v
		}
		states[i] = burg{yv, f, b, e0 / float64(n), e0 / float64(n), e0}
	}

	for m := 1; m <= p; m++ {
		var num, den float64
		for _, st := range states {
			n := len(st.y)
			// k_m = -2 Σ b[n-1]·f[n] / Σ (f[n]² + b[n-1]²)，池化所有段。
			for t := m; t < n; t++ {
				num += st.b[t-1] * st.f[t]
				den += st.f[t]*st.f[t] + st.b[t-1]*st.b[t-1]
			}
		}
		if den < 1e-12 {
			break
		}
		// Burg 反射系数 kb（误差滤波器约定，标准格型），构造保证 |kb|≤1。
		// 纯周期信号 |kb|→1 是真实谐振极点而非发散，仅在分母坍缩/非有限时停止。
		kb := -2 * num / den
		if math.IsNaN(kb) || math.IsInf(kb, 0) {
			break
		}
		// 误差滤波器系数的标准 Levinson 阶更新：
		// newα_j = α_j + kb·α_{m-j}（加号），新末位 newα_m = kb。
		newAlpha := make([]float64, m)
		for j := 1; j < m; j++ {
			newAlpha[j-1] = alpha[j-1] + kb*alpha[m-j-1]
		}
		newAlpha[m-1] = kb
		alpha = newAlpha
		// 标准 Burg 格型误差更新：
		// fₘ[t]=fₘ₋₁[t]+kb·bₘ₋₁[t-1]，bₘ[t]=bₘ₋₁[t-1]+kb·fₘ₋₁[t]。
		for i := range states {
			st := &states[i]
			n := len(st.y)
			nf := make([]float64, n)
			nb := make([]float64, n)
			for t := m; t < n; t++ {
				nf[t] = st.f[t] + kb*st.b[t-1]
				nb[t] = st.b[t-1] + kb*st.f[t]
			}
			st.f, st.b = nf, nb
		}
		effOrder = m
	}
	if effOrder == 0 {
		return make([]float64, p), false
	}
	// 误差滤波器 A(z)=1+Σαⱼz⁻ʲ 与预测器 y[k]=Σaⱼy[k-j] 的关系为 a = -α。
	for j := range alpha {
		a[j] = -alpha[j]
	}
	return a, true
}

// stableOrder 返回该动态样本集可支撑的最高稳定 LP 阶数（不超过 maxP）。
// 逐阶递推，遇反射系数不稳定或无支撑即停。
func stableOrder(x []float64, dyn []int, mu float64, maxP int) int {
	last := 0
	for p := 1; p <= maxP; p++ {
		a, ok := levinsonCoefs(x, dyn, mu, p)
		if !ok || a[p-1] == 0 {
			break
		}
		last = p
	}
	return last
}

// gapRowsCentered 在零均值空间 y=x-mu 构造缺口 [s,e] 的前向/后向 LP 方程。
// 前向：y[k] = Σ a_j y[k-j]；后向（平稳 AR 系数对称）：y[k] = Σ a_j y[k+j]。
// 每条预测方程产生一行，两侧已知样本把缺口夹在中间，系统满秩。
// 返回的系数/常数均针对中心化变量，缺口解也是零均值空间的值。
func gapRowsCentered(x []float64, s, e int, a []float64, mu float64) []lsRow {
	p := len(a)
	yc := func(k int) float64 { return x[k] - mu }
	var rows []lsRow
	// 前向方程，k 从 s 到 e+p（覆盖所有至少含一个缺口变量的方程）。
	for k := s; k <= e+p; k++ {
		if k >= len(x) {
			break
		}
		coef := map[int]float64{k: 1.0}
		rhs := 0.0
		for j := 1; j <= p; j++ {
			idx := k - j
			if idx < 0 {
				break
			}
			if idx >= s && idx <= e {
				coef[idx] -= a[j-1]
			} else {
				rhs += a[j-1] * yc(idx)
			}
		}
		rows = append(rows, lsRow{coef, rhs})
	}
	// 后向方程。
	for k := s - p; k <= e; k++ {
		if k < 0 {
			continue
		}
		coef := map[int]float64{k: 1.0}
		rhs := 0.0
		for j := 1; j <= p; j++ {
			idx := k + j
			if idx >= len(x) {
				break
			}
			if idx >= s && idx <= e {
				coef[idx] -= a[j-1]
			} else {
				rhs += a[j-1] * yc(idx)
			}
		}
		rows = append(rows, lsRow{coef, rhs})
	}
	return rows
}

// solveGapCentered 在零均值空间以前向/后向预测联合最小二乘求解缺口缺失样本。
func solveGapCentered(x []float64, s, e int, a []float64, mu float64) []float64 {
	m := e - s + 1
	rows := gapRowsCentered(x, s, e, a, mu)

	ata := make([][]float64, m)
	atb := make([]float64, m)
	for i := range ata {
		ata[i] = make([]float64, m)
	}
	for _, row := range rows {
		rhs := row.rhs
		// 缺口外的已知变量系数移入常数侧（中心化空间）。
		for gk, gv := range row.coef {
			if gk < s || gk > e {
				rhs -= gv * (x[gk] - mu)
			}
		}
		for gi := 0; gi < m; gi++ {
			gv, ok := row.coef[s+gi]
			if !ok {
				continue
			}
			atb[gi] += gv * rhs
			for gj := 0; gj < m; gj++ {
				if ov, ok := row.coef[s+gj]; ok {
					ata[gi][gj] += gv * ov
				}
			}
		}
	}
	for i := range ata {
		ata[i][i] += 1e-9 // 微小岭项保证数值正定
	}
	return gaussianSolve(ata, atb)
}

// gaussianSolve 带部分选主元的高斯消元。
func gaussianSolve(A [][]float64, b []float64) []float64 {
	n := len(b)
	M := make([][]float64, n)
	for i := range M {
		M[i] = make([]float64, n+1)
		copy(M[i], A[i])
		M[i][n] = b[i]
	}
	for col := 0; col < n; col++ {
		piv := col
		for r := col + 1; r < n; r++ {
			if math.Abs(M[r][col]) > math.Abs(M[piv][col]) {
				piv = r
			}
		}
		M[col], M[piv] = M[piv], M[col]
		if math.Abs(M[col][col]) < 1e-14 {
			M[col][col] = 1e-14
		}
		for r := 0; r < n; r++ {
			if r == col {
				continue
			}
			f := M[r][col] / M[col][col]
			for k := col; k <= n; k++ {
				M[r][k] -= f * M[col][k]
			}
		}
	}
	x := make([]float64, n)
	for i := range x {
		x[i] = M[i][n] / M[i][i]
	}
	return x
}

// gapBounds 返回该极性削波段可行的动态范围闭区间（原始信号空间）。
// 正向：[削波电平, 正满幅]；负向：[负满幅, -削波电平]。
func gapBounds(positive bool, clipLevel, fullScale int) (float64, float64) {
	if positive {
		return float64(clipLevel), float64(fullScale)
	}
	return -float64(fullScale), -float64(clipLevel)
}

// projectActiveSetCentered 在零均值空间做活动集投影：把最小二乘解逐点投影到
// [low,high]（已平移到零均值空间），被固定的点移除后重新求解，直到全部可行。
func projectActiveSetCentered(x []float64, s, e int, a []float64, mu float64, v0 []float64, low, high float64) []float64 {
	m := e - s + 1
	v := append([]float64(nil), v0...)
	fixed := map[int]float64{}
	for iter := 0; iter < m+1; iter++ {
		viol := -1
		for i := 0; i < m; i++ {
			if _, ok := fixed[i]; ok {
				continue
			}
			if v[i] < low || v[i] > high {
				viol = i
				break
			}
		}
		if viol < 0 {
			break
		}
		val := math.Max(low, math.Min(high, v[viol]))
		fixed[viol] = val
		v = reSolveCentered(x, s, e, a, mu, fixed)
	}
	for i := range v {
		v[i] = math.Max(low, math.Min(high, v[i]))
	}
	return v
}

// reSolveCentered 在部分缺口样本固定后，于零均值空间重建并求解最小二乘系统。
func reSolveCentered(x []float64, s, e int, a []float64, mu float64, fixed map[int]float64) []float64 {
	m := e - s + 1
	var free []int
	for i := 0; i < m; i++ {
		if _, ok := fixed[i]; !ok {
			free = append(free, i)
		}
	}
	v := make([]float64, m)
	for i, val := range fixed {
		v[i] = val
	}
	nf := len(free)
	if nf == 0 {
		return v
	}
	ata := make([][]float64, nf)
	atb := make([]float64, nf)
	for i := range ata {
		ata[i] = make([]float64, nf)
	}
	accum := func(row lsRow) {
		rhs := row.rhs
		for gk, gv := range row.coef {
			local := gk - s
			if local < 0 || local >= m {
				rhs -= gv * (x[gk] - mu)
				continue
			}
			if val, ok := fixed[local]; ok {
				rhs -= gv * val
			}
		}
		for fi, gidx := range free {
			gv, ok := row.coef[s+gidx]
			if !ok {
				continue
			}
			atb[fi] += gv * rhs
			for fj, oidx := range free {
				if ov, ok := row.coef[s+oidx]; ok {
					ata[fi][fj] += gv * ov
				}
			}
		}
	}
	for _, row := range gapRowsCentered(x, s, e, a, mu) {
		accum(row)
	}
	for i := range ata {
		ata[i][i] += 1e-9
	}
	sol := gaussianSolve(ata, atb)
	for fi, gidx := range free {
		v[gidx] = sol[fi]
	}
	return v
}

// VerifyPeakFloat 验证器 A（独立）：float 重建信号的峰值不得超过满幅。
// 失败时返回带位置的 ErrPeakOverflow。
func VerifyPeakFloat(x []float64, fullScale int) error {
	neg := -float64(fullScale) - 1.0 // int16 负满幅为 -32768
	pos := float64(fullScale)
	for i, v := range x {
		if v > pos || v < neg {
			return &RepairError{Err: ErrPeakOverflow, Index: i, End: i}
		}
	}
	return nil
}

// VerifyBoundaryContinuous 验证器 B（独立）：削平段两侧一阶差分连续性（C1）。
//
// 判据是一阶导数在边界处逐点衔接、不产生人工折角：
//   - 左边界：修复后的进入差分 dL=x[s]-x[s-1] 与紧邻的已知动态差分 refL 比较；
//   - 右边界：离开差分 dR=x[e+1]-x[e] 与紧邻的已知动态差分 refR 比较。
//
// 平滑信号相邻样本的差分本身会缓慢变化，故容差采用“绝对项 + 相对项”：
// tol 为绝对容差（int16 单位），再叠加参考差分绝对值的 25% 作为尺度相关
// 容差。未修复平台造成的人工折角通常是局部差分的数倍，必然越界；而正确
// 重建的平滑衔接只在自然变化范围内。长平台跨峰顶/谷底时左右斜率本就不同，
// 故两侧各自独立判断。参考差分若落在另一平台上则向外退到最近动态差分。
func VerifyBoundaryContinuous(x []float64, s, e, clipLevel int, tol float64) error {
	if s <= 1 || e >= len(x)-2 {
		return &RepairError{Err: ErrBoundaryJump, Index: s, End: e}
	}
	dL := x[s] - x[s-1]
	dR := x[e+1] - x[e]
	refL := nearestDynamicDiffLeft(x, s-1, clipLevel)
	refR := nearestDynamicDiffRight(x, e+1, clipLevel)
	bound := func(ref float64) float64 {
		return tol + 0.25*math.Abs(ref)
	}
	if math.Abs(dL-refL) > bound(refL) || math.Abs(dR-refR) > bound(refR) {
		return &RepairError{Err: ErrBoundaryJump, Index: s, End: e}
	}
	return nil
}

// nearestDynamicDiffLeft 返回以 edge 为右端点、向左最近的“两端均为动态样本”
// 的一阶差分 x[edge]-x[k]（跳过贴削波电平的平台）。
func nearestDynamicDiffLeft(x []float64, edge, clipLevel int) float64 {
	level := float64(clipLevel)
	for k := edge - 1; k >= 0; k-- {
		if math.Abs(x[edge]) < level && math.Abs(x[k]) < level {
			return x[edge] - x[k]
		}
	}
	return 0
}

// nearestDynamicDiffRight 返回以 edge 为左端点、向右最近的动态一阶差分。
func nearestDynamicDiffRight(x []float64, edge, clipLevel int) float64 {
	level := float64(clipLevel)
	for k := edge + 1; k < len(x); k++ {
		if math.Abs(x[edge]) < level && math.Abs(x[k]) < level {
			return x[k] - x[edge]
		}
	}
	return 0
}

func abs16(v int16) int16 {
	if v < 0 {
		return -v
	}
	return v
}

func clampInt16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
