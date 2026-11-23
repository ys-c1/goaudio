package audio

import (
	"errors"
	"fmt"
	"math"
)

// 新增的多声道对齐错误。原有单路错误（ErrSampleRate 等）保持不变。
var (
	// ErrTooFewChannels：声道数不足二，报出实际通道号
	ErrTooFewChannels = errors.New("声道数不足二")
	// ErrRateMismatch：采样率不一致，报出通道号与各自采样率
	ErrRateMismatch = errors.New("采样率不一致")
	// ErrDelayOutOfWindow：解出的延迟超出搜索窗口，报出通道号与延迟
	ErrDelayOutOfWindow = errors.New("延迟超出搜索窗口")
	// ErrFramesMismatch：各声道帧数不一致
	ErrFramesMismatch = errors.New("各声道帧数不一致")
)

// edgeConfidence：首遍 argmax 落在窗口内部时仍需达到的归一化相关强度；
// 低于该值视为不可信，将触发窗口加宽复核。
const edgeConfidence = 0.5

// ChannelResult 登记一路相对主声道（0 路）的对齐结果。
type ChannelResult struct {
	Channel    int     // 通道号（0 为主声道）
	Delay      int     // 相对主声道的样本延迟：lag>0 表示本路滞后主声道
	Polarity   int     // 相关峰极性：+1 同相，-1 反相，0 静音路
	Silent     bool    // 静音路（能量为零）
	PeakBefore float64 // 对齐前与主声道在零延迟处的归一化相关
	PeakAfter  float64 // 对齐后与主声道在零延迟处的归一化相关（即峰值提升核验值）
	Rounds     int     // 该路互相关搜索轮数（每个候选 lag 一轮）
	WindowW    int     // 首遍搜索窗口半宽
	WindowNote string  // 实测窗口宽度超过估计值时的理由；未加宽为空
}

// AlignReport 是一次多声道对齐的完整登记。
type AlignReport struct {
	Channels    int
	Frames      int // 对齐前后总帧数（不变量：总时长不变）
	MaxDelay    int // 估计最大延迟（首遍窗口半宽）
	Results     []ChannelResult
	Aligned     *MultiClip
	TotalRounds int // 各路搜索轮数之和
}

// MultiClip 表示等长、等采样率的多声道音频。
type MultiClip struct {
	Rate     int
	Channels [][]int16
}

// NewMultiClip 构造多声道容器，只做分配不做语义校验。
func NewMultiClip(rate int, channels, frames int) *MultiClip {
	mc := &MultiClip{Rate: rate, Channels: make([][]int16, channels)}
	for i := range mc.Channels {
		mc.Channels[i] = make([]int16, frames)
	}
	return mc
}

// AlignChannels 按互相关峰值解出每路相对主声道（0 路）的延迟，
// 再按延迟整数重采样（样本平移）对齐。
//
// 不变量（逐条在内部核验，违反即返回错误）：
//  1. 对齐后每路与主声道的零延迟归一化相关峰值不低于对齐前（非静音、
//     非零延迟路必须严格上升）；
//  2. 对齐前后总帧数（总时长）不变。
func AlignChannels(mc *MultiClip, maxDelay int) (*AlignReport, error) {
	if mc == nil || len(mc.Channels) < 2 {
		n := 0
		if mc != nil {
			n = len(mc.Channels)
		}
		return nil, fmt.Errorf("%w：实际通道号=%d（至少需要 2 路）", ErrTooFewChannels, n)
	}
	n := len(mc.Channels)
	frames := len(mc.Channels[0])
	for ch := 1; ch < n; ch++ {
		if len(mc.Channels[ch]) != frames {
			return nil, fmt.Errorf("%w：通道号=%d 帧数=%d，通道号=0 帧数=%d",
				ErrFramesMismatch, ch, len(mc.Channels[ch]), frames)
		}
	}
	if maxDelay < 1 || maxDelay >= frames {
		// 窗口估计值本身非法时退化为不超过半帧的合法值。
		maxDelay = frames / 2
		if maxDelay < 1 {
			maxDelay = 1
		}
	}

	ref := mc.Channels[0]
	report := &AlignReport{
		Channels: n,
		Frames:   frames,
		MaxDelay: maxDelay,
		Results:  make([]ChannelResult, 0, n),
		Aligned:  &MultiClip{Rate: mc.Rate, Channels: make([][]int16, n)},
	}
	// 主声道原样拷贝，不做任何修改。
	report.Aligned.Channels[0] = append([]int16(nil), ref...)
	report.Results = append(report.Results, ChannelResult{
		Channel: 0, Delay: 0, Polarity: 1,
		PeakBefore: 1, PeakAfter: 1,
		Rounds: 0, WindowW: maxDelay,
	})

	for ch := 1; ch < n; ch++ {
		res, aligned, err := alignOne(ch, ref, mc.Channels[ch], maxDelay)
		if err != nil {
			return nil, err
		}
		report.Results = append(report.Results, res)
		report.Aligned.Channels[ch] = aligned
		report.TotalRounds += res.Rounds
	}

	// 不变量 2：总时长不变，逐路核验帧数。
	for ch := 0; ch < n; ch++ {
		if len(report.Aligned.Channels[ch]) != frames {
			return nil, fmt.Errorf("不变量违反：通道号=%d 对齐后帧数=%d，期望=%d",
				ch, len(report.Aligned.Channels[ch]), frames)
		}
	}
	return report, nil
}

// alignOne 对齐单路：估计延迟 → 平移 → 双不变量核验。
func alignOne(ch int, ref, sig []int16, maxDelay int) (ChannelResult, []int16, error) {
	frames := len(ref)
	res := ChannelResult{Channel: ch, WindowW: maxDelay}

	// 静音路：能量为零，互相关无定义。延迟记 0，原样输出，不参与峰值上升核验。
	if energy(sig) == 0 {
		res.Silent = true
		res.Polarity = 0
		res.Delay = 0
		res.PeakBefore, res.PeakAfter = 0, 0
		res.WindowNote = "静音路能量为零，跳过互相关搜索"
		return res, append([]int16(nil), sig...), nil
	}

	// 首遍搜索：窗口按最大延迟估计，[-maxDelay, +maxDelay]。
	lag, strength, polarity, rounds1 := bestLag(ref, sig, maxDelay)
	res.Rounds += rounds1

	// 实测窗口宽度超过估计值的处理：
	// 仅当 argmax 顶到窗口边界，或内部峰值置信度不足时，才加宽到 2W 复核，
	// 并登记加宽理由；内部可信峰值直接接受，不做无谓加宽。
	w2 := 2 * maxDelay
	if w2 >= frames {
		w2 = frames - 1
	}
	if absInt(lag) == maxDelay || math.Abs(strength) < edgeConfidence {
		lag2, strength2, polarity2, rounds2 := bestLag(ref, sig, w2)
		res.Rounds += rounds2
		res.WindowNote = fmt.Sprintf(
			"首遍窗口半宽W=%d：argmax=%d 强度=%.4f（贴边=%v，置信阈值=%.2f），加宽至半宽=%d 复核，复核延迟=%d 强度=%.4f",
			maxDelay, lag, strength, absInt(lag) == maxDelay, edgeConfidence, w2, lag2, strength2)
		lag, strength, polarity = lag2, strength2, polarity2
		if absInt(lag) > maxDelay {
			return res, nil, fmt.Errorf(
				"%w：通道号=%d 实测延迟=%d 样本，超出估计窗口 [-%d,+%d]；加宽理由：%s",
				ErrDelayOutOfWindow, ch, lag, maxDelay, maxDelay, res.WindowNote)
		}
	} else {
		res.WindowNote = fmt.Sprintf(
			"首遍窗口半宽W=%d 内取得可信峰值 argmax=%d 强度=%.4f（阈值=%.2f），未加宽",
			maxDelay, lag, strength, edgeConfidence)
	}

	res.Delay = lag
	res.Polarity = polarity

	// 对齐前的零延迟归一化相关（峰值提升的基线，带符号；反相路为负值）。
	before := normCorrAt(ref, sig, 0)
	// 按延迟做整数重采样对齐：lag>0 表示本路滞后，左移 lag 个样本追赶，
	// 缺位补零；out[i]=sig[i+lag]。
	aligned := shiftSamples(sig, lag)
	after := normCorrAt(ref, aligned, 0)

	// 不变量 1：互相关峰值必须上升。峰值按 |归一化相关| 度量，
	// 因此反相路（负值）同样按绝对值核验；静音路已在前面放行；
	// 零延迟路允许持平，非零延迟路必须严格上升。
	if math.Abs(after) < math.Abs(before) {
		return res, nil, fmt.Errorf(
			"不变量违反：通道号=%d 对齐后相关峰值 %.6f 低于对齐前 %.6f（延迟=%d）",
			ch, after, before, lag)
	}
	if lag != 0 && math.Abs(after) <= math.Abs(before) {
		return res, nil, fmt.Errorf(
			"不变量违反：通道号=%d 非零延迟=%d 但峰值未严格上升（前=%.6f 后=%.6f）",
			ch, lag, before, after)
	}
	res.PeakBefore, res.PeakAfter = before, after
	return res, aligned, nil
}

// bestLag 在 [-w, +w] 内逐 lag 计算归一化互相关，返回 |值| 最大处的
// lag、原始带符号强度、极性与搜索轮数（每个候选 lag 计一轮）。
func bestLag(ref, sig []int16, w int) (lag int, strength float64, polarity int, rounds int) {
	best := -1.0
	bestLag := 0
	bestVal := 0.0
	for d := -w; d <= w; d++ {
		v := normCorrAt(ref, sig, d)
		rounds++
		if math.Abs(v) > best {
			best = math.Abs(v)
			bestLag = d
			bestVal = v
		}
	}
	pol := 1
	if bestVal < 0 {
		pol = -1
	}
	return bestLag, bestVal, pol, rounds
}

// normCorrAt 计算 sig 相对 ref 平移 lag 个样本后的归一化零延迟相关，
// 平移缺位补零；分母用整段能量，因此平移引入的零能量会自动惩罚错位，
// 保证对齐到真实延迟时该指标取得全局最大（峰值上升不变量的数学基础）。
func normCorrAt(ref, sig []int16, lag int) float64 {
	n := len(ref)
	var dot, eRef, eSig float64
	for i := 0; i < n; i++ {
		r := float64(ref[i])
		eRef += r * r
		s := 0.0
		j := i + lag
		if j >= 0 && j < n {
			s = float64(sig[j])
		}
		eSig += s * s
		dot += r * s
	}
	if eRef == 0 || eSig == 0 {
		return 0
	}
	return dot / math.Sqrt(eRef*eSig)
}

// shiftSamples 返回等长副本，out[i] = sig[i+delta]，缺位补零。
// delta>0 左移（本路滞后时追赶主声道），delta<0 右移。
func shiftSamples(sig []int16, delta int) []int16 {
	n := len(sig)
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		j := i + delta
		if j >= 0 && j < n {
			out[i] = sig[j]
		}
	}
	return out
}

func energy(sig []int16) float64 {
	var e float64
	for _, v := range sig {
		x := float64(v)
		e += x * x
	}
	return e
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
