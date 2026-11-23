package audio

import (
	"math"
)

// coeffFromSamples 把“固定起/停样本数”换算成一阶 IIR 系数 alpha=1-exp(-1/N)。
// N 表示一阶过程的时间常数样本数，N 越大系数越小、响应越慢。
func coeffFromSamples(n int) float64 {
	if n <= 0 {
		return 1 // 退化：一帧到位
	}
	return 1 - math.Exp(-1/float64(n))
}

// GainLimiter 是增益平滑型压限器：
// 目标增益 g*=min(1, T/|x|)，目标下降（要压）时走起音一阶平滑，
// 目标回升（要放开）时走释放一阶平滑。它本身就是一个双时间常数的
// 一阶包络/增益跟随器，时间常数必须经实测拟合得到，不使用经验值。
type GainLimiter struct {
	threshold float64
	alphaAtk  float64
	alphaRel  float64
	g         float64
}

// NewGainLimiter 构造固定起/停样本数的压限器。原有固定接口语义保持不变：
// attackSamples/releaseSamples 仍以样本数给出，内部仅换算成一阶系数。
func NewGainLimiter(threshold, attackSamples, releaseSamples int) (*GainLimiter, error) {
	if threshold <= 0 {
		return nil, ErrNonPositive
	}
	if attackSamples < 0 || releaseSamples < 0 {
		return nil, ErrFrames
	}
	return &GainLimiter{
		threshold: float64(threshold),
		alphaAtk:  coeffFromSamples(attackSamples),
		alphaRel:  coeffFromSamples(releaseSamples),
		g:         1,
	}, nil
}

// Reset 把内部增益状态复位到静止（完全放开）。
func (l *GainLimiter) Reset() { l.g = 1 }

func (l *GainLimiter) targetGain(level float64) float64 {
	if level <= l.threshold {
		return 1
	}
	return l.threshold / level
}

// Process 对一帧电平做压限，返回压限后的输出电平和该帧施加的增益。
// 起音（增益下降）与释放（增益回升）分别走各自的一阶系数。
func (l *GainLimiter) Process(level float64) (out, gain float64) {
	target := l.targetGain(level)
	if target < l.g {
		l.g += l.alphaAtk * (target - l.g)
	} else {
		l.g += l.alphaRel * (target - l.g)
	}
	return level * l.g, l.g
}

// TimeConstants 是从实测阶跃响应拟合出的一阶时间常数，同时给出
// 样本数、秒数与拟合优度 R²。
type TimeConstants struct {
	AttackTauSamples  float64
	AttackTauSeconds  float64
	AttackR2          float64
	ReleaseTauSamples float64
	ReleaseTauSeconds float64
	ReleaseR2         float64
}

// fitTau 对一阶阶跃响应的归一化残差做对数线性最小二乘拟合。
// 归一化残差 r_k=|y_k-yInf|/|y0-yInf|，一阶过程满足 ln r=-k/tau，
// 故对 (k, ln r) 做 OLS，斜率 s 即 -1/tau，tau=-1/s。
// 残差进入浮点噪声底（r<=fitFloor）后只含舍入抖动、不再服从指数律，
// 必须截断，否则会把拟合斜率拖平、tau 虚高。全程只依赖实测曲线，
// 不接受外部经验时间常数。
const fitFloor = 1e-10

func fitTau(resp []float64, y0, yInf float64) (tau, r2 float64, err error) {
	span := math.Abs(y0 - yInf)
	type pt struct {
		x, ly float64
	}
	var pts []pt
	for k, y := range resp {
		r := math.Abs(y-yInf) / span
		if r <= fitFloor || r >= 1 { // 取对数要有定义，残差须在 (fitFloor,1) 内
			continue
		}
		pts = append(pts, pt{float64(k), math.Log(r)})
	}
	m := len(pts)
	if m < 2 {
		return 0, 0, ErrFitDegenerate
	}
	var xs, ys, x2, xy float64
	for _, p := range pts {
		xs += p.x
		ys += p.ly
		x2 += p.x * p.x
		xy += p.x * p.ly
	}
	denom := float64(m)*x2 - xs*xs
	if denom == 0 {
		return 0, 0, ErrFitDegenerate
	}
	slope := (float64(m)*xy - xs*ys) / denom
	if slope >= 0 { // 一阶衰减斜率必须为负，否则不是可拟合的一阶响应
		return 0, 0, ErrFitDegenerate
	}
	tau = -1 / slope
	meanY := ys / float64(m)
	intercept := meanY - slope*xs/float64(m)
	var ssTot, ssRes float64
	for _, p := range pts {
		pred := slope*p.x + intercept
		ssTot += (p.ly - meanY) * (p.ly - meanY)
		ssRes += (p.ly - pred) * (p.ly - pred)
	}
	if ssTot == 0 {
		return 0, 0, ErrFitDegenerate
	}
	r2 = 1 - ssRes/ssTot
	return tau, r2, nil
}

// MeasureTimeConstants 实测并拟合压限器内部一阶跟随器的起音、释放时间常数。
// 方法：分别注入增益阶跃（起音：电平从阈值以下跳到 highLevel；释放：从
// highLevel 跳回阈值以下），记录每帧实测增益响应，再对实测曲线拟合 tau。
func (l *GainLimiter) MeasureTimeConstants(sampleRate, stepFrames int, highLevel float64) (TimeConstants, error) {
	if sampleRate <= 0 {
		return TimeConstants{}, ErrZeroSampleRate
	}
	if stepFrames <= 0 {
		return TimeConstants{}, ErrZeroDuration
	}
	if highLevel <= l.threshold {
		return TimeConstants{}, ErrNonPositive
	}

	// 起音阶跃：静止增益 1 -> 目标 T/highLevel，逐帧记录实测增益。
	l.Reset()
	low := l.threshold * 0.5
	l.Process(low)
	gAtkTarget := l.targetGain(highLevel)
	atkResp := make([]float64, stepFrames)
	for k := 0; k < stepFrames; k++ {
		_, g := l.Process(highLevel)
		atkResp[k] = g
	}
	tauAtk, r2Atk, err := fitTau(atkResp, 1.0, gAtkTarget)
	if err != nil {
		return TimeConstants{}, err
	}

	// 释放阶跃：先在高电平上稳态，再跳回低电平，增益 gAtkTarget -> 1。
	l.Reset()
	for k := 0; k < stepFrames; k++ {
		l.Process(highLevel)
	}
	gStart := l.g
	relResp := make([]float64, stepFrames)
	for k := 0; k < stepFrames; k++ {
		_, g := l.Process(low)
		relResp[k] = g
	}
	tauRel, r2Rel, err := fitTau(relResp, gStart, 1.0)
	if err != nil {
		return TimeConstants{}, err
	}

	return TimeConstants{
		AttackTauSamples:  tauAtk,
		AttackTauSeconds:  tauAtk / float64(sampleRate),
		AttackR2:          r2Atk,
		ReleaseTauSamples: tauRel,
		ReleaseTauSeconds: tauRel / float64(sampleRate),
		ReleaseR2:         r2Rel,
	}, nil
}

// Limit 是固定起/停样本数压限接口：用当前系数处理整段电平，返回压限后电平。
func (l *GainLimiter) Limit(levels []float64) []float64 {
	l.Reset()
	out := make([]float64, len(levels))
	for i, x := range levels {
		out[i], _ = l.Process(x)
	}
	return out
}

// AdaptiveReport 是反解结果：实测时间常数、反解出的起/停时间与实测过冲/纹波。
type AdaptiveReport struct {
	SampleRate         int
	DurationFrames     int
	Threshold          int
	AllowedOvershoot   float64
	AllowedRipple      float64
	Tau                TimeConstants
	AttackSamples      int
	AttackSeconds      float64
	ReleaseSamples     int
	ReleaseSeconds     float64
	MeasuredOvershoot  float64 // 起音后输出峰值相对阈值的超出量（电平单位）
	MeasuredRipple     float64 // 释放期间输出相对稳态目标的最大偏差（电平单位）
	PeakInput          float64
	BelowThresholdPeak float64
	Limited            bool // 该片段是否真的越过阈值、需要压限
}

// runPass 用给定系数跑完整段，返回每帧输出、增益，并统计过冲与纹波。
// 过冲：输入在阈值之上时，输出超过阈值的最大量（含起音瞬态与跟踪滞后）。
// 纹波：仅在高->低跳变后的释放恢复期统计，即增益仍 <1 却遇到阈值以下电平时，
// 输出相对“完全放开后的稳态输出 x”的偏差。稳态跟踪误差不计入纹波，
// 以保证纹波随释放时间常数单调、最大偏差落在跳变首帧。
func runPass(threshold int, alphaAtk, alphaRel float64, levels []float64) (outs, gains []float64, overshoot, ripple float64) {
	T := float64(threshold)
	g := 1.0
	n := len(levels)
	outs = make([]float64, n)
	gains = make([]float64, n)
	for i, x := range levels {
		target := 1.0
		if x > T {
			target = T / x
		}
		if target < g {
			g += alphaAtk * (target - g)
		} else {
			g += alphaRel * (target - g)
		}
		y := x * g
		outs[i] = y
		gains[i] = g
		if x > T {
			if d := y - T; d > overshoot {
				overshoot = d
			}
		} else if g < 1.0 {
			// 释放恢复期：增益尚未回到 1，输出被压低，偏差即纹波。
			if d := math.Abs(y - x); d > ripple {
				ripple = d
			}
		}
	}
	return outs, gains, overshoot, ripple
}

// searchMaxSamples 在实测整段信号上二分搜索“仍满足预算的最大样本数”。
// 一阶系数随 N 单调变化：N 越小响应越快、偏差（过冲/纹波）越小。
// pick 从一次实跑里取出要约束的那个量（过冲或纹波）。返回实测达标下的最大 N，
// 以及该 N 下实测的过冲与纹波。这是以实测曲线为真值的反解，不依赖经验取值。
func searchMaxSamples(threshold int, levels []float64, budget float64,
	useAttack bool, fixedOther int, hiEstimate float64,
) (nBest int, overshoot, ripple float64) {
	measure := func(n int) (float64, float64) {
		var aA, aR float64
		if useAttack {
			aA = coeffFromSamples(n)
			aR = coeffFromSamples(fixedOther)
		} else {
			aA = coeffFromSamples(fixedOther)
			aR = coeffFromSamples(n)
		}
		_, _, o, r := runPass(threshold, aA, aR, levels)
		return o, r
	}
	metric := func(o, r float64) float64 {
		if useAttack {
			return o
		}
		return r
	}

	lo := 0
	hi := int(hiEstimate) + 1
	// 先把 hi 撑到确实超预算（或到片段长度），保证二分区间完整。
	for {
		o, r := measure(hi)
		overshoot, ripple = o, r
		if metric(o, r) > budget || hi >= len(levels) {
			break
		}
		hi *= 2
		if hi == 0 {
			hi = len(levels)
			break
		}
	}
	best := 0
	for lo <= hi {
		mid := (lo + hi) / 2
		o, r := measure(mid)
		if metric(o, r) <= budget {
			best = mid // 达标，尝试更慢（更大 N）
			overshoot, ripple = o, r
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, overshoot, ripple
}

// VerifyAttackOvershoot 单独验证起音约束：起音后过冲不得超过 allowedOvershoot。
func VerifyAttackOvershoot(threshold, attackSamples, releaseSamples int, levels []float64, allowedOvershoot float64) (measured float64, err error) {
	if allowedOvershoot == 0 {
		return 0, ErrZeroOvershoot
	}
	if allowedOvershoot < 0 {
		return 0, ErrNonPositive
	}
	l, err := NewGainLimiter(threshold, attackSamples, releaseSamples)
	if err != nil {
		return 0, err
	}
	_, _, overshoot, _ := runPass(threshold, l.alphaAtk, l.alphaRel, levels)
	if overshoot > allowedOvershoot {
		return overshoot, ErrLimitExceeded
	}
	return overshoot, nil
}

// VerifyReleaseRipple 单独验证释放约束：释放纹波不得超过 allowedRipple。
func VerifyReleaseRipple(threshold, attackSamples, releaseSamples int, levels []float64, allowedRipple float64) (measured float64, err error) {
	if allowedRipple == 0 {
		return 0, ErrZeroRipple
	}
	if allowedRipple < 0 {
		return 0, ErrNonPositive
	}
	l, err := NewGainLimiter(threshold, attackSamples, releaseSamples)
	if err != nil {
		return 0, err
	}
	_, _, _, ripple := runPass(threshold, l.alphaAtk, l.alphaRel, levels)
	if ripple > allowedRipple {
		return ripple, ErrLimitExceeded
	}
	return ripple, nil
}

// SolveAdaptiveTimes 起停时间反解主流程。
//  1. 用固定系数构造参考一阶跟随器，注入阶跃并实测拟合出 tau_attack/tau_release，
//     再由“标称样本数 -> 实测 tau”的标定比例把反解锚定到真实一阶响应；
//  2. 由允许过冲反解起音时间上界：首帧最大过冲 = A*(1-T/A)*exp(-1/tau_a)，
//     令其 <= allowedOvershoot，求可用的最大起音样本数；
//  3. 由允许纹波反解释放时间上界：低电平峰值 B 上的最大纹波 = B*exp(-1/tau_r)，
//     令其 <= allowedRipple，求可用的最大释放样本数；
//  4. 用反解系数实跑整段，过冲、纹波两条分别独立验证，取整误差向更快方向收紧。
//
// sampleRate==0、allowedOvershoot==0、durationFrames==0 等零值一律拒绝，
// 错误信息点名具体字段。
func SolveAdaptiveTimes(sampleRate, durationFrames, threshold int, levels []float64, allowedOvershoot, allowedRipple float64) (*AdaptiveReport, error) {
	// —— 零值/非法入参拒绝，逐条点名字段 ——
	if sampleRate == 0 {
		return nil, ErrZeroSampleRate
	}
	if sampleRate < 0 {
		return nil, ErrZeroSampleRate
	}
	if durationFrames == 0 {
		return nil, ErrZeroDuration
	}
	if durationFrames < 0 {
		return nil, ErrZeroDuration
	}
	if allowedOvershoot == 0 {
		return nil, ErrZeroOvershoot
	}
	if allowedRipple == 0 {
		return nil, ErrZeroRipple
	}
	if allowedOvershoot < 0 || allowedRipple < 0 {
		return nil, ErrNonPositive
	}
	if threshold <= 0 {
		return nil, ErrNonPositive
	}
	if len(levels) == 0 {
		return nil, ErrZeroDuration
	}

	// 片段统计量。
	var peak, belowPeak float64
	for _, x := range levels {
		a := math.Abs(x)
		if a > peak {
			peak = a
		}
		if a <= float64(threshold) && a > belowPeak {
			belowPeak = a
		}
	}

	rep := &AdaptiveReport{
		SampleRate:         sampleRate,
		DurationFrames:     durationFrames,
		Threshold:          threshold,
		AllowedOvershoot:   allowedOvershoot,
		AllowedRipple:      allowedRipple,
		PeakInput:          peak,
		BelowThresholdPeak: belowPeak,
	}

	// 未越过阈值：无需压限，起停时间为 0，但时间常数仍实测给出。
	ref, err := NewGainLimiter(threshold, 64, 256)
	if err != nil {
		return nil, err
	}
	step := len(levels)
	if step < 16 {
		step = 16
	}
	tau, err := ref.MeasureTimeConstants(sampleRate, step, peak)
	if err != nil {
		// 峰值不超过阈值时构造一个确定的高电平用于拟合。
		tau, err = ref.MeasureTimeConstants(sampleRate, step, float64(threshold)*2)
		if err != nil {
			return nil, err
		}
	}
	rep.Tau = tau

	if peak <= float64(threshold) {
		return rep, nil
	}
	rep.Limited = true

	T := float64(threshold)
	A := peak
	// 实测标定：参考跟随器标称 N=64/256，实测拟合出真实 tau，
	// 得到“标称样本数 -> 实际时间常数”的比例 k，反解一律经过实测 tau，不套经验值。
	const nominalAtk, nominalRel = 64.0, 256.0
	kAtk := tau.AttackTauSamples / nominalAtk
	kRel := tau.ReleaseTauSamples / nominalRel

	// 解析上界初估（基于实测标定比例 k），再以整段实测为真值二分搜索。
	// 起音：最坏瞬间跳变下首帧过冲 overshoot_max=(A-T)*exp(-1/(k_a*N_a))；
	// 释放：低电平峰值 B 上最大纹波 ripple_max=B*exp(-1/(k_r*N_r))。
	rawOver := A - T
	var estAtk, estRel float64
	if rawOver > allowedOvershoot {
		estAtk = -1 / (kAtk * math.Log(allowedOvershoot/rawOver))
	}
	if belowPeak > allowedRipple {
		estRel = -1 / (kRel * math.Log(allowedRipple/belowPeak))
	}

	// 实测二分：先在固定释放下找达标的最大起音 N，再在该起音下找最大释放 N。
	// 鼓点瞬态陡 -> 实测很快就超预算 -> 起音 N 小；弦乐缓升 -> 可容纳更大 N。
	nAtk, overshoot, ripple := searchMaxSamples(threshold, levels, allowedOvershoot, true, int(estRel), estAtk)
	nRel, overshoot, ripple := searchMaxSamples(threshold, levels, allowedRipple, false, nAtk, estRel)

	rep.AttackSamples = nAtk
	rep.AttackSeconds = float64(nAtk) / float64(sampleRate)
	rep.ReleaseSamples = nRel
	rep.ReleaseSeconds = float64(nRel) / float64(sampleRate)
	rep.MeasuredOvershoot = overshoot
	rep.MeasuredRipple = ripple

	// 两条约束分开验，互不掩盖。
	if _, err := VerifyAttackOvershoot(threshold, nAtk, nRel, levels, allowedOvershoot); err != nil {
		return rep, err
	}
	if _, err := VerifyReleaseRipple(threshold, nAtk, nRel, levels, allowedRipple); err != nil {
		return rep, err
	}
	return rep, nil
}
