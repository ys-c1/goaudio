package audio

import "errors"

var (
	ErrSampleRate = errors.New("参数 rate 非法：采样率必须为正")
	ErrFrames     = errors.New("帧数必须为正")
	ErrFormat     = errors.New("WAV 头非法")
	ErrFreq       = errors.New("频率必须为正且低于采样率一半")
	ErrBitDepth   = errors.New("参数 bitDepth 越界：取值范围为 1..16")
	ErrSeed       = errors.New("参数 seed 不能为零")
	ErrTargetSNR  = errors.New("参数 targetSNRDB 必须为正")
)
