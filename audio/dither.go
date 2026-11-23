package audio

import (
	"math"
	"strconv"
	"strings"
)

// FullScale 为 16-bit PCM 的满幅参考电平，SNR 一律相对该电平推算。
const FullScale = 32767.0

// QuantStep 返回把 16-bit 帧量化到 bitDepth 位时的量化步长 Δ = 2^(16-bitDepth)。
// bitDepth 越界（不在 1..16）时返回 ErrBitDepth。
func QuantStep(bitDepth int) (float64, error) {
	if bitDepth < 1 || bitDepth > 16 {
		return 0, ErrBitDepth
	}
	return float64(int(1) << (16 - bitDepth)), nil
}

// NativeTPDFSNR 返回 bitDepth 位量化器叠加满格 TPDF 抖动时抖动分量的固有信噪比（dB）。
// 推导：满幅正弦信号功率 S = FullScale²/2；满格 TPDF 抖动 RMS 为 Δ/√6，
// 即抖动功率 P_d = Δ²/6。SNR = S/P_d = 3·(FullScale/Δ)² ≈ 6.02B−1.25 dB。
// 以此值作为目标 SNR 反推抖动幅度时，恰好得到 Δ/√6，与正推自洽。
func NativeTPDFSNR(bitDepth int) (float64, error) {
	delta, err := QuantStep(bitDepth)
	if err != nil {
		return 0, err
	}
	snr := 3 * (FullScale / delta) * (FullScale / delta)
	return 10 * math.Log10(snr), nil
}

// TargetDitherRMS 按目标信噪比推算抖动幅度（样本域 RMS）：
// σ = FullScale / sqrt(2 · 10^(targetSNRDB/10))。
// targetSNRDB 必须为正，bitDepth 越界返回对应参数错误。
func TargetDitherRMS(bitDepth int, targetSNRDB float64) (float64, error) {
	if _, err := QuantStep(bitDepth); err != nil {
		return 0, err
	}
	if targetSNRDB <= 0 {
		return 0, ErrTargetSNR
	}
	signalPower := FullScale * FullScale / 2
	return math.Sqrt(signalPower / math.Pow(10, targetSNRDB/10)), nil
}

// splitmix64 是自带状态的确定性伪随机源，不依赖全局 rand 状态，
// 同一 seed 产出的序列在任意 Go 版本/平台上逐位一致。
type splitmix64 struct {
	state uint64
}

func (s *splitmix64) next() uint64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// DitherSequence 生成 n 个可复现的 TPDF（三角分布，[-1,1)）抖动样本。
// 两个独立均匀量之和天然得到三角分布：(u1+u2-1) ∈ [-1,1)，
// 原始 RMS 为 1/sqrt(6) ≈ 0.408248。
// 同一种子、同一 n 两次调用逐样本相同；seed 为零返回 ErrSeed。
func DitherSequence(n int, seed uint64) ([]float64, error) {
	if seed == 0 {
		return nil, ErrSeed
	}
	rng := &splitmix64{state: seed}
	out := make([]float64, n)
	const norm = 1.0 / (1 << 53) // uint64 高 53 位 -> [0,1)
	for i := 0; i < n; i++ {
		u1 := float64(rng.next()>>11) * norm
		u2 := float64(rng.next()>>11) * norm
		out[i] = u1 + u2 - 1
	}
	return out, nil
}

// ditherSafetyMargin 为实测幅度相对推算值留出的保守余量，
// 保证有限长度样本的实测 RMS 严格不高于推算值。
const ditherSafetyMargin = 0.999

// CalibratedDither 生成标定到目标 SNR 的实际抖动序列（取整前、真正施加给样本的值）。
// 固定种子 TPDF 序列先按自身实测 RMS 归一，再缩放到推算幅度 × 0.999 安全余量，
// 因此该序列的实测 RMS 严格不高于推算值。
// targetSNRDB 传 0 时取该位深的固有 TPDF 信噪比。
func CalibratedDither(n, bitDepth int, seed uint64, targetSNRDB float64) ([]float64, float64, error) {
	delta, err := QuantStep(bitDepth)
	if err != nil {
		return nil, 0, err
	}
	if seed == 0 {
		return nil, 0, ErrSeed
	}
	if targetSNRDB == 0 {
		native, err := NativeTPDFSNR(bitDepth)
		if err != nil {
			return nil, 0, err
		}
		targetSNRDB = native
	}
	targetRMS, err := TargetDitherRMS(bitDepth, targetSNRDB)
	if err != nil {
		return nil, 0, err
	}
	raw, err := DitherSequence(n, seed)
	if err != nil {
		return nil, 0, err
	}
	var sumSq float64
	for _, v := range raw {
		sumSq += v * v
	}
	rawRMS := math.Sqrt(sumSq / float64(n))
	if rawRMS == 0 {
		// 实际不会发生（TPDF 取零为零概率事件），防御除零：回退到理论 RMS。
		rawRMS = 1 / math.Sqrt(6)
	}
	scale := targetRMS * ditherSafetyMargin / rawRMS
	out := make([]float64, n)
	for i, v := range raw {
		out[i] = v * scale
	}
	return out, delta, nil
}

// DitherQuantize 对 clip 做带固定种子 TPDF 抖动的位深压缩。
//
// 抖动幅度先由 targetSNRDB 推算目标 RMS，再把固定种子的 TPDF 序列
// 整体标定到该幅度（另乘 0.999 安全余量），因此实测幅度只可能低于推算值。
// targetSNRDB 传 0 时取该位深的固有 TPDF 信噪比。
//
// 参数错误（均带参数名）：bitDepth 越界 ErrBitDepth、seed 为零 ErrSeed、
// clip 采样率为零 ErrSampleRate、targetSNRDB 非正 ErrTargetSNR。
// 输出为新分配的帧，输入 clip 不被修改。
func DitherQuantize(c *Clip, bitDepth int, seed uint64, targetSNRDB float64) ([]int16, error) {
	if c == nil || c.Rate <= 0 {
		return nil, ErrSampleRate
	}
	dither, delta, err := CalibratedDither(len(c.Frames), bitDepth, seed, targetSNRDB)
	if err != nil {
		return nil, err
	}
	out := make([]int16, len(c.Frames))
	for i, x := range c.Frames {
		q := math.Round((float64(x)+dither[i])/delta) * delta
		if q > math.MaxInt16 {
			q = math.MaxInt16
		} else if q < math.MinInt16 {
			q = math.MinInt16
		}
		out[i] = int16(q)
	}
	return out, nil
}

// Quantize 为无抖动（直通/舍入）量化基线：仅按步长四舍五入并夹幅，
// 不引入任何噪声。bitDepth=16 时输出与输入逐样本相同。
// 原有直通语义保持不变，本函数只是把该语义显式化以便对照。
func Quantize(c *Clip, bitDepth int) ([]int16, error) {
	delta, err := QuantStep(bitDepth)
	if err != nil {
		return nil, err
	}
	if c == nil || c.Rate <= 0 {
		return nil, ErrSampleRate
	}
	out := make([]int16, len(c.Frames))
	for i, x := range c.Frames {
		q := math.Round(float64(x)/delta) * delta
		if q > math.MaxInt16 {
			q = math.MaxInt16
		} else if q < math.MinInt16 {
			q = math.MinInt16
		}
		out[i] = int16(q)
	}
	return out, nil
}

// SilenceRatio 返回严格零样本占比（最朴素的静音口径）。
func SilenceRatio(frames []int16) float64 {
	if len(frames) == 0 {
		return 0
	}
	z := 0
	for _, v := range frames {
		if v == 0 {
			z++
		}
	}
	return float64(z) / float64(len(frames))
}

// DeadbandSilenceRatio 返回落在量化器零死区 |x| < delta/2 内的样本占比。
// 这与无抖动量化器“判零”的口径完全一致，是核验静音段比例不变量的公平基准。
func DeadbandSilenceRatio(frames []int16, delta float64) float64 {
	if len(frames) == 0 {
		return 0
	}
	z := 0
	half := delta / 2
	for _, v := range frames {
		if math.Abs(float64(v)) < half {
			z++
		}
	}
	return float64(z) / float64(len(frames))
}

// RMS 返回帧的均方根幅度。
func RMS(frames []int16) float64 {
	if len(frames) == 0 {
		return 0
	}
	var sumSq float64
	for _, v := range frames {
		sumSq += float64(v) * float64(v)
	}
	return math.Sqrt(sumSq / float64(len(frames)))
}

// MeasuredSNR 返回输出相对满幅电平的实测信噪比（dB）。
func MeasuredSNR(frames []int16) float64 {
	r := RMS(frames)
	if r <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(r/FullScale)
}

// ErrorDist 汇总输出相对输入的逐样本误差（输出-输入）分布。
type ErrorDist struct {
	Delta   float64 // 量化步长
	Bins    []int   // 12 个内箱，每箱宽 Δ/2，覆盖 [-3Δ,3Δ)
	Under   int     // 误差 < -3Δ 的样本数
	Over    int     // 误差 >= 3Δ 的样本数
	Mean    float64 // 误差均值
	RMS     float64 // 误差 RMS
	Min     float64 // 最小误差
	Max     float64 // 最大误差
	Samples int     // 样本总数
}

// NewErrorDist 统计量化误差分布。delta 为量化步长。
func NewErrorDist(in, out []int16, delta float64) ErrorDist {
	const bins = 12
	d := ErrorDist{Delta: delta, Bins: make([]int, bins), Min: math.Inf(1), Max: math.Inf(-1)}
	width := delta / 2
	var sum, sumSq float64
	for i := range in {
		e := float64(out[i]) - float64(in[i])
		d.Samples++
		sum += e
		sumSq += e * e
		if e < d.Min {
			d.Min = e
		}
		if e > d.Max {
			d.Max = e
		}
		idx := int((e + 3*delta) / width)
		if idx < 0 {
			d.Under++
		} else if idx >= bins {
			d.Over++
		} else {
			d.Bins[idx]++
		}
	}
	if d.Samples > 0 {
		d.Mean = sum / float64(d.Samples)
		d.RMS = math.Sqrt(sumSq / float64(d.Samples))
	}
	return d
}

// String 以文本条形图渲染误差分布，箱宽 Δ/2，越界样本单列。
func (d ErrorDist) String() string {
	var b []byte
	maxCount := 0
	for _, c := range d.Bins {
		if c > maxCount {
			maxCount = c
		}
	}
	b = append(b, []byte("  误差区间(单位Δ)    样本数  分布\n")...)
	for i, c := range d.Bins {
		lo := -3 + float64(i)*0.5
		hi := lo + 0.5
		bar := strings.Repeat("█", scaleBar(c, maxCount))
		b = append(b, []byte(formatBin(lo, hi, c, bar))...)
	}
	if d.Under > 0 || d.Over > 0 {
		b = append(b, []byte(formatBin(-99, -3, d.Under, "越界<-3Δ"))...)
		b = append(b, []byte(formatBin(3, 99, d.Over, "越界>=3Δ"))...)
	}
	b = append(b, []byte(formatStat("均值", d.Mean/d.Delta))...)
	b = append(b, []byte(formatStat("RMS", d.RMS/d.Delta))...)
	b = append(b, []byte(formatStat("最小", d.Min/d.Delta))...)
	b = append(b, []byte(formatStat("最大", d.Max/d.Delta))...)
	return string(b)
}

func scaleBar(count, maxCount int) int {
	if maxCount == 0 {
		return 0
	}
	const maxWidth = 40
	return (count*maxWidth + maxCount/2) / maxCount
}

func formatBin(lo, hi float64, count int, bar string) string {
	if lo < -90 {
		return "  (< -3.0Δ)        " + padInt(count) + "  " + bar + "\n"
	}
	if hi > 90 {
		return "  (>=  3.0Δ)        " + padInt(count) + "  " + bar + "\n"
	}
	return "  [" + padDelta(lo) + ", " + padDelta(hi) + ")  " + padInt(count) + "  " + bar + "\n"
}

func formatStat(name string, valInDelta float64) string {
	return "  " + name + ": " + strconv.FormatFloat(valInDelta, 'f', 4, 64) + " Δ\n"
}

func padInt(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 6 {
		s = " " + s
	}
	return s
}

func padDelta(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	for len(s) < 5 {
		s = " " + s
	}
	return s
}
