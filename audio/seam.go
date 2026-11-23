package audio

// SeamSample 是接缝不变量“接缝两侧样本差必须为零”的逐样本记录。
type SeamSample struct {
	Name     string // entry.left / entry.right / exit.left / exit.right
	OutIndex int    // 输出 Clip 中的下标
	Out      int16  // 输出样本
	Src      int16  // 对应源素材样本
	Diff     int    // |Out − Src|，不变量要求每条都为 0
}

// SeamSamples 逐样本给出交叉淡化四个接缝点的输出与源素材之差。
// 任一下标越界（例如淡化区已覆盖整条单样本素材）的点会被跳过，
// 调用方据此可以逐样本核验 Diff 是否全为 0。
func SeamSamples(a, b, out *Clip, fadeFrames int) []SeamSample {
	nF := len(a.Frames)
	entryL := nF - fadeFrames - 1
	entryR := nF - fadeFrames
	exitL := nF - 1
	exitR := nF
	type point struct {
		name string
		idx  int
		src  int16
	}
	var pts []point
	if entryL >= 0 {
		pts = append(pts, point{"entry.left", entryL, a.Frames[entryL]})
	}
	if entryR < nF {
		// 淡化区首样本权重为 (1,0)，源侧取 A。
		pts = append(pts, point{"entry.right", entryR, a.Frames[entryR]})
	}
	if exitL >= 0 && exitL >= entryR {
		// 淡化区末样本权重为 (0,1)，源侧取 B。
		pts = append(pts, point{"exit.left", exitL, b.Frames[fadeFrames-1]})
	}
	if exitR < len(out.Frames) {
		pts = append(pts, point{"exit.right", exitR, b.Frames[fadeFrames]})
	}
	res := make([]SeamSample, 0, len(pts))
	for _, p := range pts {
		d := int(out.Frames[p.idx]) - int(p.src)
		if d < 0 {
			d = -d
		}
		res = append(res, SeamSample{
			Name:     p.name,
			OutIndex: p.idx,
			Out:      out.Frames[p.idx],
			Src:      p.src,
			Diff:     d,
		})
	}
	return res
}

// Jump 是一对相邻样本的逐点跳变记录。
type Jump struct {
	LeftIndex int   // 左样本下标
	Left      int16 // 左样本值
	Right     int16 // 右样本值
	AbsDiff   int   // |Right − Left|
}

// Jumps 逐点列出输出区间 [from, to] 内的相邻样本跳变：
// 第 k 行对应 (out[k], out[k+1])，k 从 from 到 to−1。
func Jumps(out *Clip, from, to int) ([]Jump, error) {
	n := len(out.Frames)
	if from < 0 || to > n || from >= to {
		return nil, &FieldError{
			Field: "jumpRange",
			Msg:   "跳变统计区间非法: from=" + itoa(from) + ", to=" + itoa(to) + ", frames=" + itoa(n),
			Err:   ErrRange,
		}
	}
	jumps := make([]Jump, 0, to-from)
	for k := from; k < to-1; k++ {
		d := int(out.Frames[k+1]) - int(out.Frames[k])
		if d < 0 {
			d = -d
		}
		jumps = append(jumps, Jump{
			LeftIndex: k,
			Left:      out.Frames[k],
			Right:     out.Frames[k+1],
			AbsDiff:   d,
		})
	}
	return jumps, nil
}

// SeamJumps 列出接缝区逐点跳变：入口跨越对、淡化区内部各对、出口跨越对。
// 硬切（fadeFrames == 0）只列出拼接点一对。
func SeamJumps(a, out *Clip, fadeFrames int) ([]Jump, error) {
	nF := len(a.Frames)
	if fadeFrames == 0 {
		return Jumps(out, nF-1, nF+1)
	}
	from := nF - fadeFrames - 1
	to := nF + 1
	if from < 0 {
		from = 0
	}
	if to > len(out.Frames) {
		to = len(out.Frames)
	}
	return Jumps(out, from, to)
}

// MaxJump 返回跳变序列中的最大绝对跳变及其所在左样本下标；空序列返回 0/-1。
func MaxJump(jumps []Jump) (maxAbs int, atLeftIndex int) {
	atLeftIndex = -1
	for _, j := range jumps {
		if j.AbsDiff > maxAbs {
			maxAbs = j.AbsDiff
			atLeftIndex = j.LeftIndex
		}
	}
	return maxAbs, atLeftIndex
}

// CheckMaxJump 按阈值判定：最大跳变超过 threshold 即失败，
// 返回包装了 ErrJumpTooLarge 且带字段名的 FieldError。
func CheckMaxJump(jumps []Jump, threshold int) error {
	maxAbs, at := MaxJump(jumps)
	if maxAbs > threshold {
		return &FieldError{
			Field: "maxJump",
			Msg: "接缝最大跳变超过阈值: maxJump=" + itoa(maxAbs) +
				" @样本 " + itoa(at) + ", threshold=" + itoa(threshold),
			Err: ErrJumpTooLarge,
		}
	}
	return nil
}
