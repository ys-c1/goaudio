package audio

import "fmt"

// MultiClipFromClips 把若干单路 Clip 组装成多声道。
// 三类非法输入之一——采样率不一致——在此报出具体通道号；
// 声道数不足二与帧数不一致在 AlignChannels 内统一报出。
func MultiClipFromClips(clips ...*Clip) (*MultiClip, error) {
	if len(clips) < 2 {
		return nil, fmt.Errorf("%w：实际通道号=%d（至少需要 2 路）",
			ErrTooFewChannels, len(clips))
	}
	rate := clips[0].Rate
	for ch := 1; ch < len(clips); ch++ {
		if clips[ch].Rate != rate {
			return nil, fmt.Errorf("%w：通道号=%d 采样率=%dHz，通道号=0 采样率=%dHz",
				ErrRateMismatch, ch, clips[ch].Rate, rate)
		}
	}
	mc := &MultiClip{Rate: rate, Channels: make([][]int16, len(clips))}
	for ch, c := range clips {
		mc.Channels[ch] = append([]int16(nil), c.Frames...)
	}
	return mc, nil
}
