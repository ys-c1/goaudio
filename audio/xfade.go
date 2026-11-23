// Package audio 的交叉淡化实现。
//
// 相位按单位圆第一象限弧长均分：
//
//	phase(i) = (π/2) · i/(n−1),  i ∈ [0, n−1]
//
// 两侧权重由相位在单位圆上的坐标直接分配：wa = cos(phase)、wb = sin(phase)。
// 因而除端点外恒有 wa²+wb² = 1（等功率），端点被精确钉为 (1,0)/(0,1)，
// 保证淡化区入口与出口处输出与源素材逐位相等。
package audio

import "math"

// arcWeights 返回淡化区第 i 个样本（共 n 个）的两侧权重。
// n == 1 时取第一象限角平分点 π/4，两侧权重均为 √2/2。
func arcWeights(i, n int) (wa, wb float64) {
	if n <= 1 {
		w := math.Sqrt(0.5)
		return w, w
	}
	if i == 0 {
		return 1, 0
	}
	if i == n-1 {
		return 0, 1
	}
	phase := math.Pi / 2 * float64(i) / float64(n-1)
	return math.Cos(phase), math.Sin(phase)
}

// blend 是逐样本混合的唯一算术出口：out = round(wa·a + wb·b)，
// 饱和到 int16，调用方的逐样本不变量核验都以本函数为准。
func blend(a, b int16, wa, wb float64) int16 {
	v := wa*float64(a) + wb*float64(b)
	r := math.Round(v)
	if r > math.MaxInt16 {
		return math.MaxInt16
	}
	if r < math.MinInt16 {
		return math.MinInt16
	}
	return int16(r)
}

// XFade 在 a、b 之间做相位对齐的交叉淡化，返回新 Clip，不修改入参。
//
// 输出长度为 len(a)+len(b)−fadeFrames：a 的头部原样保留，
// 末 fadeFrames 帧与 b 的前 fadeFrames 帧按弧长权重混合，b 的尾部原样保留。
//
// 错误（均带字段名）：
//   - fadeFrames 为零或负 → ErrFadeZero（字段 fadeFrames）
//   - 两侧 Rate 不一致    → ErrRateMismatch（字段 sampleRate）
//   - 任一 Clip 无帧      → ErrFrames（字段 framesA / framesB）
//   - fadeFrames 超过任一侧长度 → ErrFadeTooLong（字段 fadeFrames）
func XFade(a, b *Clip, fadeFrames int) (*Clip, error) {
	if err := validateInputs(a, b, fadeFrames); err != nil {
		return nil, err
	}
	nF := len(a.Frames)
	nB := len(b.Frames)
	out := make([]int16, nF+nB-fadeFrames)

	// a 的非重叠头部，逐位复制。
	copy(out[:nF-fadeFrames], a.Frames[:nF-fadeFrames])

	// 淡化区：按弧长相位分配权重。
	for i := 0; i < fadeFrames; i++ {
		wa, wb := arcWeights(i, fadeFrames)
		out[nF-fadeFrames+i] = blend(a.Frames[nF-fadeFrames+i], b.Frames[i], wa, wb)
	}

	// b 的非重叠尾部，逐位复制。
	copy(out[nF:], b.Frames[fadeFrames:])
	return &Clip{Rate: a.Rate, Frames: out}, nil
}

// HardCut 做无淡化拼接（fadeFrames 为 0 的显式对照路径）。
// 除淡化时长外的输入校验与 XFade 一致。
func HardCut(a, b *Clip) (*Clip, error) {
	if a == nil || b == nil {
		return nil, &FieldError{Field: "clip", Msg: "输入 Clip 不能为 nil", Err: ErrFrames}
	}
	if a.Rate != b.Rate {
		return nil, &FieldError{
			Field: "sampleRate",
			Msg:   "两侧采样率不一致: rateA=" + itoa(a.Rate) + ", rateB=" + itoa(b.Rate),
			Err:   ErrRateMismatch,
		}
	}
	if len(a.Frames) == 0 {
		return nil, &FieldError{Field: "framesA", Msg: "A 侧帧数为 0", Err: ErrFrames}
	}
	if len(b.Frames) == 0 {
		return nil, &FieldError{Field: "framesB", Msg: "B 侧帧数为 0", Err: ErrFrames}
	}
	out := make([]int16, 0, len(a.Frames)+len(b.Frames))
	out = append(out, a.Frames...)
	out = append(out, b.Frames...)
	return &Clip{Rate: a.Rate, Frames: out}, nil
}

func validateInputs(a, b *Clip, fadeFrames int) error {
	if a == nil || b == nil {
		return &FieldError{Field: "clip", Msg: "输入 Clip 不能为 nil", Err: ErrFrames}
	}
	if fadeFrames <= 0 {
		return &FieldError{
			Field: "fadeFrames",
			Msg:   "淡化时长不能为零: fadeFrames=" + itoa(fadeFrames),
			Err:   ErrFadeZero,
		}
	}
	if a.Rate != b.Rate {
		return &FieldError{
			Field: "sampleRate",
			Msg:   "两侧采样率不一致: rateA=" + itoa(a.Rate) + ", rateB=" + itoa(b.Rate),
			Err:   ErrRateMismatch,
		}
	}
	if len(a.Frames) == 0 {
		return &FieldError{Field: "framesA", Msg: "A 侧帧数为 0", Err: ErrFrames}
	}
	if len(b.Frames) == 0 {
		return &FieldError{Field: "framesB", Msg: "B 侧帧数为 0", Err: ErrFrames}
	}
	if fadeFrames > len(a.Frames) || fadeFrames > len(b.Frames) {
		return &FieldError{
			Field: "fadeFrames",
			Msg: "淡化重叠帧数超过素材长度: fadeFrames=" + itoa(fadeFrames) +
				", framesA=" + itoa(len(a.Frames)) + ", framesB=" + itoa(len(b.Frames)),
			Err: ErrFadeTooLong,
		}
	}
	return nil
}

func itoa(v int) string {
	// 避免为错误消息再引入 strconv 的包装歧义；手写最小整数转字符串。
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
