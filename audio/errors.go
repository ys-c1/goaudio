package audio

import "errors"

var (
	ErrSampleRate = errors.New("采样率必须为正")
	ErrFrames     = errors.New("帧数必须为正")
	ErrFormat     = errors.New("WAV 头非法")
	ErrFreq       = errors.New("频率必须为正且低于采样率一半")

	// 削波修复相关错误，均通过 RepairError 携带发生位置。
	ErrConfig       = errors.New("修复配置非法")
	ErrFlatLimit    = errors.New("削平段长度超过上限")
	ErrZeroOrder    = errors.New("预测阶数估计为零，邻域无动态信息")
	ErrEdgeClip     = errors.New("削平段贴近信号边缘，缺少一侧已知样本")
	ErrPeakOverflow = errors.New("修复后峰值超过满幅")
	ErrBoundaryJump = errors.New("削平段两侧一阶差分不连续")
)
