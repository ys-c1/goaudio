package audio

import "math"

func Sine(c *Clip, freq float64, amp int) error {
	if freq <= 0 || freq >= float64(c.Rate)/2 {
		return ErrFreq
	}
	if amp < 0 || amp > 32767 {
		amp = 32767
	}
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		c.Frames[i] = int16(float64(amp) * math.Sin(2*math.Pi*freq*t))
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
