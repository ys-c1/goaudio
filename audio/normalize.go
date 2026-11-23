package audio

import (
	"math"
	"strconv"
)

// FullScale 是 16-bit PCM 的对称满幅幅值。
const FullScale = 32767.0

// 目标响度允许的 dBFS 区间下界（再低已接近数字静音，无实际意义）。
const minTargetLUFS = -120.0

// ParamError 携带非法参数的名称，便于调用方定位具体参数。
type ParamError struct {
	Param string
	Msg   string
}

func (e *ParamError) Error() string {
	return "参数 " + e.Param + " 非法: " + e.Msg
}

// NormalizeConfig 描述一次响度归一的输入参数。
type NormalizeConfig struct {
	// TargetLUFS 目标响度（单声道 RMS，dBFS 计），有效区间 [-120, 0)。
	TargetLUFS float64
	// ClipProb 允许的削顶概率，有效区间 (0, 1)。
	ClipProb float64
	// SegmentLen 分段长度（帧）；<=0 表示整段作为一个分段。
	SegmentLen int
}

// SegmentStat 是单个分段的归一统计。
type SegmentStat struct {
	Index      int
	Start      int
	End        int
	Gain       float64 // 实际使用的线性增益（整段增益与分段增益合并后）
	LoudBefore float64 // 归一前响度 dBFS，静音为 -Inf
	LoudAfter  float64 // 归一后响度 dBFS，静音为 -Inf
	PeakBefore int
	PeakAfter  int
}

// DistBin 是幅值分布表的一个区间（dBFS 计）。
type DistBin struct {
	Label       string
	BeforeCount int
	AfterCount  int
	BeforeFrac  float64
	AfterFrac   float64
}

// NormalizeReport 汇总一次归一的全部可核验数据。
type NormalizeReport struct {
	TargetLUFS     float64
	ClipProb       float64
	GlobalGain     float64 // 由整段能量解出的线性增益
	Threshold      int     // 由削顶概率反解、实际使用的限幅阈值（int16 幅值单位）
	ScaledPeak     int     // 限幅前缩放信号的峰值（用于核验反解阈值的口径）
	ClipCount      int     // 实际触发限幅的样本数
	ActualClipProb float64
	LoudBefore     float64
	LoudAfter      float64
	PeakBefore     int
	PeakAfter      int
	Segments       []SegmentStat
	Distribution   []DistBin
}

// ApplyGain 对整段素材施加统一的线性增益，结果饱和限制在满幅之内。
// 这是整体增益接口，签名与行为保持稳定，归一化在其之外另行实现。
func ApplyGain(c *Clip, gain float64) error {
	if c == nil || len(c.Frames) == 0 {
		return &ParamError{Param: "frames", Msg: "时长（帧数）必须为正"}
	}
	if gain < 0 {
		return &ParamError{Param: "gain", Msg: "增益不能为负"}
	}
	for i, x := range c.Frames {
		v := int(math.Round(float64(x) * gain))
		if v > 32767 {
			v = 32767
		}
		if v < -32767 {
			v = -32767
		}
		c.Frames[i] = int16(v)
	}
	return nil
}

// rmsLinear 返回线性满幅刻度下的 RMS 幅值；空切片或全零返回 0。
func rmsLinear(frames []int16) float64 {
	var sumSq float64
	for _, v := range frames {
		f := float64(v)
		sumSq += f * f
	}
	if len(frames) == 0 || sumSq == 0 {
		return 0
	}
	return math.Sqrt(sumSq / float64(len(frames)))
}

// LoudnessDB 返回以满幅为 0 dBFS 的 RMS 响度；静音返回 -Inf。
func LoudnessDB(frames []int16) float64 {
	rms := rmsLinear(frames)
	if rms == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(rms / FullScale)
}

// invertLimiterThreshold 由允许的削顶概率在实际输出（整数幅值）域反解限幅阈值。
// 返回满足“超过阈值的样本占比 <= clipProb”的最大整数阈值，且不超过满幅 32767。
// 反解与实际限幅使用同一离散分布，因此实际阈值恒等于反解值，不会超过它。
func invertLimiterThreshold(hist []int, clipProb float64) int {
	n := 0
	any := false
	for m, c := range hist {
		n += c
		if m > 0 && c > 0 {
			any = true
		}
	}
	// 纯静音无需限幅，直接给满幅阈值。
	if !any {
		return 32767
	}
	maxExceed := int(math.Floor(clipProb * float64(n)))

	exceed := 0 // 当前阈值 T 之下，幅值 > T 的样本数
	T := 32767
	for T > 0 {
		if exceed+hist[T] > maxExceed {
			break
		}
		exceed += hist[T]
		T--
	}
	return T
}

// 分布表的 dBFS 区间上边界（对应 bin 1..6；bin 0 为静音）。
var distEdges = []float64{-60, -48, -36, -24, -12, 0}

func distIndex(mag int) int {
	if mag == 0 {
		return 0
	}
	m := float64(mag)
	for i, db := range distEdges {
		if m <= FullScale*math.Pow(10, db/20) {
			return i + 1
		}
	}
	return len(distEdges)
}

func distLabel(i int) string {
	if i == 0 {
		return "静音 (=0)"
	}
	hi := distEdges[i-1]
	if i == 1 {
		return "(0, " + strconv.FormatFloat(hi, 'f', 0, 64) + " dBFS]"
	}
	lo := distEdges[i-2]
	return "(" + strconv.FormatFloat(lo, 'f', 0, 64) + ", " +
		strconv.FormatFloat(hi, 'f', 0, 64) + " dBFS]"
}

// peakAbs 返回切片内样本的最大绝对幅值，按对称满幅口径把 -32768 夹为 32767，
// 保证任何 int16 输入的峰值都不超过满幅。
func peakAbs(frames []int16) int {
	m := 0
	for _, v := range frames {
		a := absInt16(v)
		if a > m {
			m = a
		}
	}
	return m
}

// Normalize 按整段与分段能量对素材做响度归一，并以限幅保证不超过满幅。
//
// 增益求解：
//  1. 整段 RMS 能量解出全局增益 gGlobal = targetAmp / rmsAll；
//  2. 每个分段由自身 RMS 解出分段增益 gSeg = targetAmp / rmsSeg；
//  3. 分段实际增益取 min(gGlobal, gSeg)：全局增益负责把整体响度拉到目标，
//     分段增益只对超过目标的响段做进一步衰减，静音段不放大噪声。
//
// 限幅阈值由允许的削顶概率在实际输出幅值分布上反解（见 invertLimiterThreshold），
// 实际阈值即反解值且 <= 32767，超过阈值的样本被硬限制到阈值。
// 整个过程只依赖输入帧数据，无随机量、无 map 遍历，同一素材多次运行结果逐位一致。
func Normalize(c *Clip, cfg NormalizeConfig) (*NormalizeReport, error) {
	if c == nil || len(c.Frames) == 0 {
		return nil, &ParamError{Param: "frames", Msg: "时长（帧数）必须为正"}
	}
	if c.Rate <= 0 {
		return nil, &ParamError{Param: "rate", Msg: "采样率必须为正"}
	}
	if cfg.TargetLUFS >= 0 || cfg.TargetLUFS < minTargetLUFS {
		return nil, &ParamError{Param: "targetLUFS", Msg: "目标响度必须落在 [-120, 0) dBFS"}
	}
	if cfg.ClipProb <= 0 || cfg.ClipProb >= 1 {
		return nil, &ParamError{Param: "clipProb", Msg: "削顶概率必须落在 (0, 1)"}
	}
	if cfg.SegmentLen < 0 {
		return nil, &ParamError{Param: "segmentLen", Msg: "分段长度不能为负"}
	}

	n := len(c.Frames)
	segLen := cfg.SegmentLen
	if segLen <= 0 {
		segLen = n
	}

	// 保留归一前快照，分段前响度/前峰值/分布均基于它。
	before := make([]int16, n)
	copy(before, c.Frames)

	loudBefore := LoudnessDB(before)
	peakBefore := peakAbs(before)
	targetAmp := FullScale * math.Pow(10, cfg.TargetLUFS/20)

	// 1) 整段能量 -> 全局增益。
	globalGain := 1.0
	if r := rmsLinear(before); r > 0 {
		globalGain = targetAmp / r
	}

	// 2) 分段能量 -> 分段增益，与全局增益取 min 合并。
	type segment struct {
		start, end int
		gain       float64
	}
	segs := make([]segment, 0, (n+segLen-1)/segLen)
	for start := 0; start < n; start += segLen {
		end := start + segLen
		if end > n {
			end = n
		}
		g := globalGain
		if r := rmsLinear(before[start:end]); r > 0 {
			if need := targetAmp / r; need < g {
				g = need
			}
		}
		segs = append(segs, segment{start: start, end: end, gain: g})
	}

	// 3) 缩放后的整数幅值直方图，用于在实际输出域反解限幅阈值。
	// 反解、限幅判定、削顶计数全部基于同一离散量（四舍五入后的整数幅值）。
	// 输入幅值统一走对称满幅口径（-32768 -> 32767）。
	hist := make([]int, 32768) // 下标 0..32767
	scaled := make([]int, n)  // 每个样本缩放后的整数幅值（符号方向另由原帧决定）
	si := 0
	for i, x := range before {
		for i >= segs[si].end {
			si++
		}
		m := int(math.Round(float64(absInt16(x)) * segs[si].gain))
		if m > 32767 {
			m = 32767
		}
		scaled[i] = m
		hist[m]++
	}
	threshold := invertLimiterThreshold(hist, cfg.ClipProb)
	scaledPeak := 0
	for m := 32767; m >= 0; m-- {
		if hist[m] > 0 {
			scaledPeak = m
			break
		}
	}

	// 4) 限幅写出：整数幅值超过阈值则硬限制到阈值，再兜底夹到满幅。
	clipCount := 0
	for i, x := range before {
		m := scaled[i]
		if m > threshold {
			clipCount++
			m = threshold
		}
		if m > 32767 {
			m = 32767
		}
		if x < 0 {
			c.Frames[i] = int16(-m)
		} else {
			c.Frames[i] = int16(m)
		}
	}

	// 5) 分段统计（前值取自快照，后值取自改写后的帧）。
	segStats := make([]SegmentStat, len(segs))
	for i, s := range segs {
		segStats[i] = SegmentStat{
			Index:      i,
			Start:      s.start,
			End:        s.end,
			Gain:       s.gain,
			LoudBefore: LoudnessDB(before[s.start:s.end]),
			LoudAfter:  LoudnessDB(c.Frames[s.start:s.end]),
			PeakBefore: peakAbs(before[s.start:s.end]),
			PeakAfter:  peakAbs(c.Frames[s.start:s.end]),
		}
	}

	// 6) 归一前后幅值分布表。
	bins := make([]DistBin, len(distEdges)+1)
	for i := range bins {
		bins[i].Label = distLabel(i)
	}
	for _, x := range before {
		bins[distIndex(absInt16(x))].BeforeCount++
	}
	for _, x := range c.Frames {
		bins[distIndex(absInt16(x))].AfterCount++
	}
	for i := range bins {
		bins[i].BeforeFrac = float64(bins[i].BeforeCount) / float64(n)
		bins[i].AfterFrac = float64(bins[i].AfterCount) / float64(n)
	}

	return &NormalizeReport{
		TargetLUFS:     cfg.TargetLUFS,
		ClipProb:       cfg.ClipProb,
		GlobalGain:     globalGain,
		Threshold:      threshold,
		ScaledPeak:     scaledPeak,
		ClipCount:      clipCount,
		ActualClipProb: float64(clipCount) / float64(n),
		LoudBefore:     loudBefore,
		LoudAfter:      LoudnessDB(c.Frames),
		PeakBefore:     peakBefore,
		PeakAfter:      peakAbs(c.Frames),
		Segments:       segStats,
		Distribution:   bins,
	}, nil
}

// absInt16 返回对称满幅口径下的绝对幅值：-32768 夹为 32767。
func absInt16(v int16) int {
	if v == -32768 {
		return 32767
	}
	a := int(v)
	if a < 0 {
		return -a
	}
	return a
}
