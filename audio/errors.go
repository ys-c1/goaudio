package audio

import "errors"

var (
	ErrSampleRate   = errors.New("采样率必须为正")
	ErrFrames       = errors.New("帧数必须为正")
	ErrFormat       = errors.New("WAV 头非法")
	ErrFreq         = errors.New("频率必须为正且低于采样率一半")
	ErrFadeZero     = errors.New("淡化时长不能为零")
	ErrRateMismatch = errors.New("两侧采样率不一致")
	ErrFadeTooLong  = errors.New("淡化重叠帧数超过素材长度")
	ErrJumpTooLarge = errors.New("接缝最大跳变超过阈值")
	ErrRange        = errors.New("跳变统计区间非法")
)

// FieldError 在哨兵错误之外携带触发错误的字段名与具体取值，
// 保证“时长为零 / 采样率不一致 / 重叠超长”各返回带字段名的错误。
type FieldError struct {
	Field string // 出错字段名，如 fadeFrames、sampleRate
	Msg   string // 含具体取值的说明
	Err   error  // 被包装的哨兵错误
}

func (e *FieldError) Error() string {
	return e.Field + ": " + e.Msg
}

func (e *FieldError) Unwrap() error {
	return e.Err
}
