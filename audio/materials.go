package audio

import "math"

// MaterialKind 标识测试素材类型。
type MaterialKind string

const (
	MaterialDrum    MaterialKind = "drum"    // 鼓点：快速瞬态、密集峰值
	MaterialVocal   MaterialKind = "vocal"   // 人声：调制较慢、持续过阈
	MaterialStrings MaterialKind = "strings" // 弦乐：慢起音、长持续
	MaterialSilence MaterialKind = "silence" // 静音：全程近零电平
	MaterialTiny    MaterialKind = "tiny"    // 极短片段：帧数极少
)

// clampInt16 把 float 电平夹到 int16 范围。
func clampInt16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(math.Round(v))
}

// MakeMaterial 合成五类标准测试素材，全部使用给定采样率与帧数。
// 鼓点/人声/弦乐都会越过 threshold，静音与极短片段用于边界检验。
func MakeMaterial(kind MaterialKind, rate, frames, threshold int) (*Clip, error) {
	if rate <= 0 {
		return nil, ErrZeroSampleRate
	}
	if frames <= 0 {
		return nil, ErrZeroDuration
	}
	c, err := NewClip(rate, frames)
	if err != nil {
		return nil, err
	}
	loud := float64(threshold) * 2 // 稳定越过阈值
	switch kind {
	case MaterialDrum:
		// 每 200ms 一次快速衰减的鼓点，瞬态极快。
		period := rate / 5
		for i := range c.Frames {
			phase := i % period
			env := math.Exp(-12 * float64(phase) / float64(period))
			c.Frames[i] = clampInt16(loud * env * math.Sin(2*math.Pi*120*float64(i)/float64(rate)))
		}
	case MaterialVocal:
		// 带慢振幅起伏的持续音，大部分时间在阈值之上。
		for i := range c.Frames {
			t := float64(i) / float64(rate)
			env := 0.75 + 0.25*math.Sin(2*math.Pi*3*t)
			c.Frames[i] = clampInt16(loud * env * math.Sin(2*math.Pi*300*t))
		}
	case MaterialStrings:
		// 慢起音长持续：前 300ms 线性爬升到满电平后保持。
		ramp := rate * 3 / 10
		if ramp > frames {
			ramp = frames
		}
		for i := range c.Frames {
			env := 1.0
			if i < ramp {
				env = float64(i) / float64(ramp)
			}
			c.Frames[i] = clampInt16(loud * env * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		}
	case MaterialSilence:
		// 全程零电平，叠一个极小的底噪（远低于阈值）。
		for i := range c.Frames {
			c.Frames[i] = clampInt16(float64(threshold) * 0.01 * math.Sin(2*math.Pi*50*float64(i)/float64(rate)))
		}
	case MaterialTiny:
		// 极短片段：8 帧，电平从低跳到高，专门考验短数据下的拟合与反解。
		for i := range c.Frames {
			v := float64(threshold) * 0.2
			if i >= 4 {
				v = loud
			}
			c.Frames[i] = clampInt16(v)
		}
	default:
		return nil, ErrNonPositive
	}
	return c, nil
}

// AbsLevels 取整段帧的绝对值电平，供反解接口使用。
func AbsLevels(c *Clip) []float64 {
	out := make([]float64, len(c.Frames))
	for i, v := range c.Frames {
		x := float64(v)
		if x < 0 {
			x = -x
		}
		out[i] = x
	}
	return out
}
