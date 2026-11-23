package audio

import "errors"

var (
	ErrSampleRate = errors.New("采样率必须为正")
	ErrFrames     = errors.New("帧数必须为正")
	ErrFormat     = errors.New("WAV 头非法")
	ErrFreq       = errors.New("频率必须为正且低于采样率一半")

	// 反解接口的入参错误，错误信息点名出问题的字段。
	ErrZeroSampleRate = errors.New("字段 sampleRate 不得为零：采样率必须为正")
	ErrZeroDuration   = errors.New("字段 durationFrames 不得为零：片段时长必须为正")
	ErrZeroOvershoot  = errors.New("字段 allowedOvershoot 不得为零：允许过冲必须为正（零过冲要求零起音时间，不可实现）")
	ErrZeroRipple     = errors.New("字段 allowedRipple 不得为零：允许纹波必须为正（零纹波要求无限释放时间，不可实现）")
	ErrNonPositive    = errors.New("阈值/系数必须为正")
	ErrFitDegenerate  = errors.New("一阶响应曲线退化，无法拟合时间常数")
	ErrLimitExceeded  = errors.New("实测值超过允许值")
)
