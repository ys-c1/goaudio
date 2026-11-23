package audio

import "errors"

var (
	ErrSampleRate = errors.New("采样率必须为正")
	ErrFrames     = errors.New("帧数必须为正")
	ErrFormat     = errors.New("WAV 头非法")
	ErrFreq       = errors.New("频率必须为正且低于采样率一半")
	ErrTarget     = errors.New("目标误差上界必须在 (0,1) 内")
	ErrKernelLen  = errors.New("核长必须为正奇数")
	ErrOversample = errors.New("过采样率必须为正")
)
