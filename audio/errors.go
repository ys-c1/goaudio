package audio

import (
	"errors"
	"strconv"
)

var (
	ErrSampleRate = errors.New("采样率必须为正")
	ErrFrames     = errors.New("帧数必须为正")
	ErrFormat     = errors.New("WAV 头非法")
	ErrFreq       = errors.New("频率必须为正且低于采样率一半")

	// 端点检测参数越界。具体是哪个参数由 ParamError 指明。
	ErrWindow      = errors.New("窗长必须为正")
	ErrWindowBound = errors.New("实际窗长超过允许漏检率反解出的上限")
	ErrHop         = errors.New("帧移必须为正且不超过窗长一半")
	ErrDuration    = errors.New("时长必须为正")
	ErrMissRate    = errors.New("允许漏检率必须在 (0,1) 内")
	ErrThreshold   = errors.New("判决门限系数非法")
	ErrClip        = errors.New("素材为空")
)

// ParamError 指明越界的参数名与取值，便于调用方定位。
type ParamError struct {
	Param string
	Value float64
	Err   error
}

func (e *ParamError) Error() string {
	return e.Param + "=" + formatFloat(e.Value) + " 越界: " + e.Err.Error()
}

func (e *ParamError) Unwrap() error { return e.Err }

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func paramErr(param string, value float64, err error) error {
	return &ParamError{Param: param, Value: value, Err: err}
}
