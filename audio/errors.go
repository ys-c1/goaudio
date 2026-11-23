package audio

import "errors"

var (
	ErrSampleRate = errors.New("采样率必须为正")
	ErrFrames     = errors.New("帧数必须为正")
	ErrFormat     = errors.New("WAV 头非法")
	ErrFreq       = errors.New("频率必须为正且低于采样率一半")

	ErrThreshold = errors.New("门限必须为负（dBFS 满幅为 0）")
	ErrRatio     = errors.New("压缩比必须为正")
	ErrKnee      = errors.New("拐点宽度不能为负")
	ErrDuration  = errors.New("检测时长必须为正")
	ErrReduction = errors.New("目标压缩量非法或超出实测范围")

	ErrClip      = errors.New("音频片段为空")
	ErrLevels    = errors.New("电平序列为空")
	ErrMeasure   = errors.New("测量时长不足，至少覆盖四个检测窗")
	ErrSlope     = errors.New("拐点两侧斜率超出给定区间")
	ErrSlopeData = errors.New("缺少拐点某一侧的实测斜率")
	ErrIdentify  = errors.New("实测数据不足以唯一确定拐点宽度")
)
