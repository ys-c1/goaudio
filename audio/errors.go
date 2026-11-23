package audio

import "errors"

var (
	ErrSampleRate   = errors.New("采样率必须为正")
	ErrFrames       = errors.New("帧数必须为正")
	ErrFormat       = errors.New("WAV 头非法")
	ErrFreq         = errors.New("频率必须为正且低于采样率一半")
	ErrThreshold    = errors.New("门限非法：必须为正（1..32767）")
	ErrRatio        = errors.New("比率非法：不能为负或 NaN（膝宽/攻击/释放等标量参数同归此类，必须非负）")
	ErrDuration     = errors.New("片段时长不足：帧数必须大于攻击与释放帧数之和，且至少 2 帧")
	ErrRateMismatch = errors.New("源与目标采样率不一致")
	ErrSizeMismatch = errors.New("源与目标帧数不一致")
)
