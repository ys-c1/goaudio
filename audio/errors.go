package audio

import "errors"

var (
	ErrSampleRate = errors.New("采样率必须为正")
	ErrFrames     = errors.New("帧数必须为正")
	ErrFormat     = errors.New("WAV 头非法")
	ErrFreq       = errors.New("频率必须为正且低于采样率一半")
)
