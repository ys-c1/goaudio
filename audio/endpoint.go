package audio

// 端点检测（VAD）：短时能量 + 过零率双门限、迟滞判决、短段合并。
//
// 本文件只做增量。原有 Clip/NewClip/SaveWAV/LoadWAV/Sine/ApplyEnvelope/Peak
// 的签名与语义保持不变（整体分析接口不受影响）。
//
// # 门限如何解出
//
// 逐帧计算短时能量（RMS，按 int16 满幅归一为 [0,1]）与过零率：
//
//	E[k] = sqrt( mean(x[i]^2) ) / 32768
//	Z[k] = (窗内相邻样本符号变化次数) / 窗长
//
// 对能量序列做一维 Otsu 二分类，取类间方差最大处相邻两值的中点为能量门限；
// 再用低分位能量估计噪声底 noise。于是
//
//	低门限 = max(noise·EnergyLowK, 绝对底)
//	高门限 = max(Otsu 门限, noise·EnergyHighK, 低门限)
//
// 过零率门限取噪声帧（能量 ≤ 低门限）的 Z 均值 + 3σ，再夹到 [0.25, 0.45]：
// 几百 Hz 的正弦类浊音过零率约 0.05~0.3，白噪声约 0.5，故该门限能挡住
// 能量高但过零率同样高的噪声爆发。用户给出的 ZCRMax > 0 时直接采用。
//
// # 窗长如何由允许漏检率反解
//
// 一帧只有在一个长为 W 的窗里累计出足够能量才会被判为有声。若门限把窗内
// 的有效发声漏掉，则该帧不发声；于是任一真实发声段的首、尾各有一段宽度
// 不超过 W 的样本可能落在检测段之外（覆盖该样本的窗起点都在 [x-W, x] 内，
// 全部未被判决即可漏检）。两端合计漏检 ≤ 2W，故对时长 T ≥ T_min 的段
//
//	miss(T) ≤ 2W / T ≤ 2W / T_min
//
// 要求漏检率 ≤ p，只需 2W ≤ p·T_min，即 W ≤ p·T_min/2。
// SolveWindowBound 按此反解窗长上限；实际窗长超过反解值时报 ErrWindowBound
// （ParamError 指明 windowLen），不做静默截断。这是无条件上界：门限正确时
// 帧内能量充足，实际漏检通常为 0（组段还会把边界向外扩一个帧移 H）。
//
// # 短段如何合并
//
// 先做迟滞判决：能量 ≥ 高门限起段，之后能量 ≥ 低门限则延续，否则收段；
// 过零率超限的帧一律不算有声。相邻发声游程之间静音短于 MinSilenceMs 的
// 合并（覆盖发声内部的短暂掉帧），合并后仍短于 MinSpeechMs 的段丢弃。
// 静音段取有声段在整段素材上的补集，因此始终无缝覆盖整段素材。
//
// 合并只在「间隔 < MinSilenceMs」时发生，故两次真实发声之间只要静音显著
// 长于 MinSilenceMs，检测结果就不会跨越该静音。

import (
	"math"
	"sort"
	"strconv"
)

// Segment 是半开采样区间 [Start, End)。
type Segment struct {
	Start int
	End   int
}

// Duration 返回样本数。
func (s Segment) Duration() int { return s.End - s.Start }

// EndpointConfig 端点检测参数。时长单位均为毫秒。
type EndpointConfig struct {
	WindowMs        float64 // 分析窗长，> 0 且 ≤ 反解上限
	HopMs           float64 // 帧移，> 0 且 2×HopMs ≤ WindowMs
	MinSilenceMs    float64 // 短于该值的相邻发声间隔合并
	MinSpeechMs     float64 // 最短真实发声时长 T_min；短于此值的段丢弃
	AllowedMissRate float64 // 允许漏检率 p ∈ (0,1)
	EnergyLowK      float64 // 低门限 = 噪声底 × 该倍数
	EnergyHighK     float64 // 高门限 ≥ 噪声底 × 该倍数
	ZCRMax          float64 // > 0 时作为过零率上限；=0 时由噪声统计解出
}

// DefaultEndpointConfig：p=5%、T_min=400ms 反解得窗长上限
// 0.05×400/2 = 10ms，实际取 8ms、帧移 4ms（2H = W），留出裕量。
func DefaultEndpointConfig() EndpointConfig {
	return EndpointConfig{
		WindowMs:        8,
		HopMs:           4,
		MinSilenceMs:    200,
		MinSpeechMs:     400,
		AllowedMissRate: 0.05,
		EnergyLowK:      2.0,
		EnergyHighK:     6.0,
	}
}

// EndpointResult 端点检测结果与解出的统计量。
type EndpointResult struct {
	Speech  []Segment // 有声段，按时间升序，互不重叠
	Silence []Segment // 静音段，为有声段在整段素材上的补集

	WindowSamples   int     // 实际窗长（样本）
	WindowBound     int     // 由漏检率反解出的窗长上限（样本）
	AllowedMissRate float64 // 反解所用的 p
	FrameCount      int     // 帧数
	FrameHop        int     // 帧移（样本）

	NoiseFloor float64 // 噪声底（归一 RMS）
	EnergyLow  float64 // 低门限
	EnergyHigh float64 // 高门限
	ZCRLimit   float64 // 过零率上限
}

// SpeechSamples 返回被判为有声的总样本数。
func (r *EndpointResult) SpeechSamples() int {
	n := 0
	for _, s := range r.Speech {
		n += s.Duration()
	}
	return n
}

// SilenceSamples 返回被判为静音的总样本数。
func (r *EndpointResult) SilenceSamples() int {
	n := 0
	for _, s := range r.Silence {
		n += s.Duration()
	}
	return n
}

// AllowedMissRateFor 返回本次配置下反解所用 p，供报告展示。
func (c EndpointConfig) AllowedMissRateFor() float64 { return c.AllowedMissRate }

// SolveWindowBound 由允许漏检率反解窗长上限（样本）：2W ≤ p·T_min。
// 两端各最多漏一个窗长，故按 2W 计入。
func SolveWindowBound(rate int, minSpeechMs, missRate float64) (int, error) {
	if rate <= 0 {
		return 0, paramErr("sampleRate", float64(rate), ErrSampleRate)
	}
	if minSpeechMs <= 0 {
		return 0, paramErr("minSpeechMs", minSpeechMs, ErrDuration)
	}
	if missRate <= 0 || missRate >= 1 {
		return 0, paramErr("missRate", missRate, ErrMissRate)
	}
	tMin := float64(rate) * minSpeechMs / 1000.0
	b := int(math.Floor(missRate * tMin / 2))
	if b < 1 {
		return 0, paramErr("missRate", missRate, ErrMissRate)
	}
	return b, nil
}

// DetectEndpoints 对整段素材做端点检测。
func DetectEndpoints(c *Clip, cfg EndpointConfig) (*EndpointResult, error) {
	fr, err := analyzeFrames(c, cfg)
	if err != nil {
		return nil, err
	}
	n := len(c.Frames)
	res := fr.result
	if len(fr.labels) == 0 {
		res.Silence = []Segment{{0, n}}
		return &res, nil
	}
	if !anyTrue(fr.labels) {
		res.Silence = []Segment{{Start: 0, End: n}}
		return &res, nil
	}

	spans := spansFromLabels(fr.labels, fr.starts, fr.win, fr.hop, n)
	spans = mergeSpans(spans, msToSamples(c.Rate, cfg.MinSilenceMs))
	spans = dropShort(spans, msToSamples(c.Rate, cfg.MinSpeechMs))

	res.Speech = spans
	res.Silence = complement(n, spans)
	return &res, nil
}

// frameAnalysis 是分帧分析的中转结构。
type frameAnalysis struct {
	result EndpointResult
	starts []int
	win    int
	hop    int
	labels []bool
}

// analyzeFrames 做参数校验、分帧、门限求解与帧级迟滞判决。
func analyzeFrames(c *Clip, cfg EndpointConfig) (*frameAnalysis, error) {
	// —— 参数越界拦截：采样率、时长、窗长优先，且指明具体参数 ——
	if c == nil {
		return nil, paramErr("clip", 0, ErrClip)
	}
	if c.Rate <= 0 {
		return nil, paramErr("sampleRate", float64(c.Rate), ErrSampleRate)
	}
	if len(c.Frames) == 0 {
		return nil, paramErr("duration", 0, ErrDuration)
	}
	if cfg.WindowMs <= 0 {
		return nil, paramErr("windowLen", cfg.WindowMs, ErrWindow)
	}
	// 四舍五入后不足 1 个样本的窗长同样按「窗长为零」拒绝，不静默抬到 1。
	winF := float64(c.Rate) * cfg.WindowMs / 1000.0
	if winF < 0.5 {
		return nil, paramErr("windowLen", cfg.WindowMs, ErrWindow)
	}
	win := int(math.Round(winF))
	if cfg.HopMs <= 0 {
		return nil, paramErr("hopMs", cfg.HopMs, ErrHop)
	}
	hopF := float64(c.Rate) * cfg.HopMs / 1000.0
	if hopF < 0.5 {
		return nil, paramErr("hopMs", cfg.HopMs, ErrHop)
	}
	hop := int(math.Round(hopF))
	if 2*hop > win {
		return nil, paramErr("hopMs", cfg.HopMs, ErrHop)
	}
	if cfg.MinSpeechMs <= 0 {
		return nil, paramErr("minSpeechMs", cfg.MinSpeechMs, ErrDuration)
	}
	if cfg.MinSilenceMs <= 0 {
		return nil, paramErr("minSilenceMs", cfg.MinSilenceMs, ErrDuration)
	}
	if cfg.AllowedMissRate <= 0 || cfg.AllowedMissRate >= 1 {
		return nil, paramErr("missRate", cfg.AllowedMissRate, ErrMissRate)
	}
	if cfg.EnergyLowK <= 0 || cfg.EnergyHighK <= 0 {
		return nil, paramErr("energyK", math.Min(cfg.EnergyLowK, cfg.EnergyHighK), ErrThreshold)
	}
	if cfg.EnergyHighK < cfg.EnergyLowK {
		return nil, paramErr("energyHighK", cfg.EnergyHighK, ErrThreshold)
	}
	if cfg.ZCRMax < 0 || cfg.ZCRMax > 1 {
		return nil, paramErr("zcrMax", cfg.ZCRMax, ErrThreshold)
	}

	bound, err := SolveWindowBound(c.Rate, cfg.MinSpeechMs, cfg.AllowedMissRate)
	if err != nil {
		return nil, err
	}
	if win > bound {
		return nil, paramErr("windowLen", cfg.WindowMs,
			wrappedError(ErrWindowBound, win, bound, c.Rate))
	}

	starts := frameStarts(len(c.Frames), win, hop)
	energy, zcr := frameStats(c, starts, win)

	fr := &frameAnalysis{starts: starts, win: win, hop: hop}
	res := &fr.result
	res.WindowSamples = win
	res.WindowBound = bound
	res.AllowedMissRate = cfg.AllowedMissRate
	res.FrameCount = len(starts)
	res.FrameHop = hop

	floor := percentile(energy, 0.15)
	peakE := percentile(energy, 0.95)
	res.NoiseFloor = floor

	// 全静音或无动态范围：最高分位也贴着噪声底，不产生任何有声段。
	if peakE <= floor*1.5+4*absEnergyFloor {
		res.EnergyLow = math.Max(floor*cfg.EnergyLowK, absEnergyFloor*cfg.EnergyLowK)
		res.EnergyHigh = math.Max(floor*cfg.EnergyHighK, res.EnergyLow*1.5)
		res.ZCRLimit = cfg.ZCRMax
		if res.ZCRLimit == 0 {
			res.ZCRLimit = clamp(zcrMeanSigma(energy, zcr, res.EnergyLow), 0.25, 0.45)
		}
		fr.labels = make([]bool, len(starts))
		return fr, nil
	}

	low := math.Max(floor*cfg.EnergyLowK, absEnergyFloor*cfg.EnergyLowK)
	high := math.Max(otsuThreshold(energy), floor*cfg.EnergyHighK)
	if high <= low {
		high = low * 1.5
	}
	res.EnergyLow = low
	res.EnergyHigh = high
	res.ZCRLimit = cfg.ZCRMax
	if res.ZCRLimit == 0 {
		res.ZCRLimit = clamp(zcrMeanSigma(energy, zcr, low), 0.25, 0.45)
	}

	// 迟滞判决：高门限起段，低门限延续；过零率超限的帧不算有声。
	labels := make([]bool, len(starts))
	active := false
	for k := range labels {
		ok := zcr[k] <= res.ZCRLimit
		if active {
			if energy[k] >= low && ok {
				labels[k] = true
			} else {
				active = false
			}
		} else if energy[k] >= high && ok {
			labels[k] = true
			active = true
		}
	}
	fr.labels = labels
	return fr, nil
}

const absEnergyFloor = 5e-5 // 约 1.6 LSB 的 RMS；低于此按数字静音处理

func msToSamples(rate int, ms float64) int {
	n := int(math.Round(float64(rate) * ms / 1000.0))
	if n < 1 {
		n = 1
	}
	return n
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

func wrappedError(base error, win, bound, rate int) error {
	return &detailError{base: base, win: win, bound: bound, rate: rate}
}

// detailError 在窗长越界时给出样本数与毫秒数，便于定位。
type detailError struct {
	base       error
	win, bound int
	rate       int
}

func (e *detailError) Error() string {
	return e.base.Error() + ": 实际窗长 " + strconv.Itoa(e.win) + " 样本 (" +
		strconv.FormatFloat(1000*float64(e.win)/float64(e.rate), 'f', 2, 64) + " ms) > 上限 " +
		strconv.Itoa(e.bound) + " 样本 (" +
		strconv.FormatFloat(1000*float64(e.bound)/float64(e.rate), 'f', 2, 64) + " ms)"
}

func (e *detailError) Unwrap() error { return e.base }

// frameStarts 生成帧起点：步进 H；若末帧未覆盖素材末尾则补一帧对齐末尾。
func frameStarts(n, win, hop int) []int {
	if n <= 0 {
		return nil
	}
	if n <= win {
		return []int{0}
	}
	var starts []int
	for s := 0; s+win <= n; s += hop {
		starts = append(starts, s)
	}
	if last := starts[len(starts)-1] + win; last < n {
		starts = append(starts, n-win)
	}
	return starts
}

func frameStats(c *Clip, starts []int, win int) (energy, zcr []float64) {
	n := len(c.Frames)
	energy = make([]float64, len(starts))
	zcr = make([]float64, len(starts))
	for k, start := range starts {
		end := start + win
		if end > n {
			end = n
		}
		var sumSq float64
		var crossings int
		prev := c.Frames[start]
		for i := start; i < end; i++ {
			v := float64(c.Frames[i])
			sumSq += v * v
			if i > start && ((v >= 0) != (prev >= 0)) {
				crossings++
			}
			prev = c.Frames[i]
		}
		m := end - start
		energy[k] = math.Sqrt(sumSq/float64(m)) / 32768.0
		zcr[k] = float64(crossings) / float64(m)
	}
	return energy, zcr
}

func sortedCopy(v []float64) []float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s
}

// percentile 线性插值取分位数；空切片返回 0。
func percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := sortedCopy(v)
	if q <= 0 {
		return s[0]
	}
	if q >= 1 {
		return s[len(s)-1]
	}
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return s[lo]
	}
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}

// otsuThreshold 在能量序列上做一维 Otsu，返回最佳分割点。
func otsuThreshold(v []float64) float64 {
	s := sortedCopy(v)
	n := len(s)
	if n == 0 {
		return 0
	}
	var total float64
	for _, x := range s {
		total += x
	}
	var sum0 float64
	best := -1.0
	bestT := s[0]
	for i := 0; i < n-1; i++ {
		sum0 += s[i]
		if s[i] == s[i+1] {
			continue
		}
		n0 := float64(i + 1)
		n1 := float64(n - i - 1)
		d := sum0/n0 - (total-sum0)/n1
		if score := n0 * n1 * d * d; score > best {
			best = score
			bestT = (s[i] + s[i+1]) / 2
		}
	}
	return bestT
}

// zcrMeanSigma 用能量 ≤ low 的帧作噪声帧，返回其过零率均值 + 3σ。
func zcrMeanSigma(energy, zcr []float64, low float64) float64 {
	var zs []float64
	for k := range energy {
		if energy[k] <= low {
			zs = append(zs, zcr[k])
		}
	}
	if len(zs) == 0 {
		zs = zcr
	}
	var sum float64
	for _, z := range zs {
		sum += z
	}
	mean := sum / float64(len(zs))
	var variance float64
	for _, z := range zs {
		d := z - mean
		variance += d * d
	}
	return mean + 3*math.Sqrt(variance/float64(len(zs)))
}

// spansFromLabels 把帧级判决转成样本区间，并向外各扩一个帧移。
func spansFromLabels(labels []bool, starts []int, win, hop, n int) []Segment {
	var out []Segment
	i := 0
	for i < len(labels) {
		if !labels[i] {
			i++
			continue
		}
		j := i
		for j < len(labels) && labels[j] {
			j++
		}
		start := starts[i] - hop
		end := starts[j-1] + win + hop
		if start < 0 {
			start = 0
		}
		if end > n {
			end = n
		}
		if end > start {
			out = append(out, Segment{Start: start, End: end})
		}
		i = j
	}
	return out
}

func mergeSpans(spans []Segment, minSilence int) []Segment {
	if len(spans) == 0 {
		return nil
	}
	out := []Segment{spans[0]}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s.Start-last.End < minSilence {
			if s.End > last.End {
				last.End = s.End
			}
		} else {
			out = append(out, s)
		}
	}
	return out
}

func dropShort(spans []Segment, minSpeech int) []Segment {
	var out []Segment
	for _, s := range spans {
		if s.Duration() >= minSpeech {
			out = append(out, s)
		}
	}
	return out
}

func complement(n int, speech []Segment) []Segment {
	var out []Segment
	prev := 0
	for _, s := range speech {
		if s.Start > prev {
			out = append(out, Segment{Start: prev, End: s.Start})
		}
		if s.End > prev {
			prev = s.End
		}
	}
	if prev < n {
		out = append(out, Segment{Start: prev, End: n})
	}
	return out
}

func anyTrue(v []bool) bool {
	for _, b := range v {
		if b {
			return true
		}
	}
	return false
}
