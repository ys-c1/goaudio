package audio

import "errors"

var (
	ErrSampleRate = errors.New("采样率必须为正")
	ErrFrames     = errors.New("帧数必须为正")
	ErrFormat     = errors.New("WAV 头非法")
	ErrFreq       = errors.New("频率必须为正且低于采样率一半")

	ErrNoChannels   = errors.New("声道数为零：至少需要一个声道")
	ErrRateMismatch = errors.New("采样率不一致")
	ErrEmptyChannel = errors.New("声道为空")
	ErrAllNegative  = errors.New("矩阵系数全为负")
	ErrCorrelation  = errors.New("下混后左右声道相关系数超出给定区间")
	ErrCancellation = errors.New("存在单独发声的声道被完全抵消")
)
