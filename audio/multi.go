package audio

import "math"

// MultiClip 表示多声道素材，每个声道是一个独立的单声道 Clip，
// 因此各声道可以携带不同的采样率，由下混入口负责核验。
type MultiClip struct {
	Channels []*Clip
}

// StereoClip 表示下混后的两声道结果。
type StereoClip struct {
	Rate  int
	Left  []int16
	Right []int16
}

// NewMultiClip 组装多声道素材，声道数为零直接报错并指名问题。
func NewMultiClip(channels ...*Clip) (*MultiClip, error) {
	if len(channels) == 0 {
		return nil, ErrNoChannels
	}
	for i, c := range channels {
		if c == nil || len(c.Frames) == 0 {
			return nil, errf("%w：第 %d 声道", ErrEmptyChannel, i+1)
		}
	}
	return &MultiClip{Channels: channels}, nil
}

// checkRates 核验全部声道采样率一致，不一致时报错并指名冲突的两个声道。
func (m *MultiClip) checkRates() error {
	base := m.Channels[0].Rate
	for i := 1; i < len(m.Channels); i++ {
		if m.Channels[i].Rate != base {
			return errf("%w：第 1 声道为 %d Hz，第 %d 声道为 %d Hz",
				ErrRateMismatch, base, i+1, m.Channels[i].Rate)
		}
	}
	return nil
}

// nframes 以最短视频道为公共长度，越界声道按零样本处理（平移不改变能量核验语义）。
func (m *MultiClip) nframes() int {
	n := len(m.Channels[0].Frames)
	for _, c := range m.Channels[1:] {
		if len(c.Frames) < n {
			n = len(c.Frames)
		}
	}
	return n
}

// DownmixSum 是原有的直接相加接口：全部声道等权相加，左右各得一份。
// 不做任何能量校正，反相素材会自然相消——该朴素语义保持不变。
func (m *MultiClip) DownmixSum() (*StereoClip, error) {
	if len(m.Channels) == 0 {
		return nil, ErrNoChannels
	}
	if err := m.checkRates(); err != nil {
		return nil, err
	}
	n := m.nframes()
	rate := m.Channels[0].Rate
	out := &StereoClip{Rate: rate, Left: make([]int16, n), Right: make([]int16, n)}
	for t := 0; t < n; t++ {
		var sum int32
		for _, c := range m.Channels {
			sum += int32(c.Frames[t])
		}
		v := saturateInt16(sum)
		out.Left[t] = v
		out.Right[t] = v
	}
	return out, nil
}

func saturateInt16(v int32) int16 {
	switch {
	case v > math.MaxInt16:
		return math.MaxInt16
	case v < math.MinInt16:
		return math.MinInt16
	default:
		return int16(v)
	}
}
