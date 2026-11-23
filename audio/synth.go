package audio

import "math"

// Sine 保持原单曲路径与输出不变，等价于初相位 0 的 SinePhase。
func Sine(c *Clip, freq float64, amp int) error {
	return SinePhase(c, freq, amp, 0)
}

// SinePhase 在 Sine 基础上增加初相位 phase（弧度），用于构造硬切处相位错开的素材。
func SinePhase(c *Clip, freq float64, amp int, phase float64) error {
	if freq <= 0 || freq >= float64(c.Rate)/2 {
		return ErrFreq
	}
	if amp < 0 || amp > 32767 {
		amp = 32767
	}
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		// 统一使用四舍五入取整（与交叉淡化 blend 的 round 规则一致），
		// 避免 int16(float) 向零截断带来的 1 LSB 偏差。
		c.Frames[i] = int16(math.Round(float64(amp) * math.Sin(2*math.Pi*freq*t+phase)))
	}
	return nil
}

func ApplyEnvelope(c *Clip, attack, release int) error {
	n := len(c.Frames)
	if attack < 0 || release < 0 || attack+release > n {
		return ErrFrames
	}
	for i := 0; i < attack; i++ {
		c.Frames[i] = int16(int32(c.Frames[i]) * int32(i) / int32(attack))
	}
	for i := 0; i < release; i++ {
		idx := n - 1 - i
		c.Frames[idx] = int16(int32(c.Frames[idx]) * int32(i) / int32(release))
	}
	return nil
}

func Peak(c *Clip) int {
	m := 0
	for _, v := range c.Frames {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > m {
			m = a
		}
	}
	return m
}
