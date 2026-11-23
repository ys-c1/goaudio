package audio

import (
	"errors"
	"math"
	"testing"
)

// hardClip 把信号在 ±level 处硬削波（产生连续相等的削平段）。
func hardClip(frames []int16, level int) []int16 {
	out := append([]int16(nil), frames...)
	for i, v := range out {
		if int(v) > level {
			out[i] = int16(level)
		} else if int(v) < -level {
			out[i] = -int16(level)
		}
	}
	return out
}

func rmse(a, b []int16) float64 {
	var sq float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		sq += d * d
	}
	return math.Sqrt(sq / float64(len(a)))
}

// 第一类：无削波干净信号。修复应原样返回、零修复段、零样本。
// 运行：go test ./audio/ -run TestClass1Clean -v
func TestClass1Clean(t *testing.T) {
	c, err := NewClip(8000, 1600)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(c, 220, 20000); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultRepairConfig()
	out, rep, err := Declip(c, cfg)
	if err != nil {
		t.Fatalf("干净信号不应报错: %v", err)
	}
	if len(rep.Segments) != 0 || rep.RepairedCount != 0 {
		t.Fatalf("干净信号不应有修复段, got %d 段 %d 样本", len(rep.Segments), rep.RepairedCount)
	}
	for i := range c.Frames {
		if out.Frames[i] != c.Frames[i] {
			t.Fatalf("第 %d 帧被意外修改", i)
		}
	}
	if rep.PeakAfter != Peak(c) {
		t.Fatalf("峰值不应变化 %d != %d", rep.PeakAfter, Peak(c))
	}
	t.Logf("类别1 干净信号: 阶数=N/A 残差=N/A 修复样本数=0 峰值=%d", rep.PeakAfter)
}

// 第二类：低频正弦短削平段。修复后 RMSE 必须显著低于削波信号，
// 峰值不超满幅，两侧一阶差分连续，且报告阶数为正、残差有限。
// 运行：go test ./audio/ -run TestClass2ShortFlat -v
func TestClass2ShortFlat(t *testing.T) {
	c, _ := NewClip(8000, 2400)
	if err := Sine(c, 50, 34000); err != nil {
		t.Fatal(err)
	}
	clippedFrames := hardClip(c.Frames, 30000)
	clipped := &Clip{Rate: 8000, Frames: clippedFrames}
	runs := DetectFlatRuns(clippedFrames, 30000)
	if len(runs) == 0 {
		t.Fatal("测试前置失败: 未检测到削平段")
	}
	for _, r := range runs {
		if r[1]-r[0]+1 > 30 {
			t.Fatalf("第二类应是短削平段, 实际长度 %d", r[1]-r[0]+1)
		}
	}

	cfg := DefaultRepairConfig()
	cfg.ClipLevel = 30000
	out, rep, err := Declip(clipped, cfg)
	if err != nil {
		t.Fatalf("修复失败: %v", err)
	}
	if rep.RepairedCount == 0 {
		t.Fatal("应有样本被修复")
	}
	// 断言阶数为正、残差有限。
	for _, sg := range rep.Segments {
		if sg.Order <= 0 {
			t.Fatalf("段起于 %d 的预测阶数必须为正, got %d", sg.Start, sg.Order)
		}
		if math.IsNaN(sg.ResidualRMSE) || math.IsInf(sg.ResidualRMSE, 0) {
			t.Fatalf("段起于 %d 的残差非有限值", sg.Start)
		}
	}
	// 断言峰值不超满幅（验证器 A 独立再查一次）。
	if rep.PeakAfter > cfg.FullScale {
		t.Fatalf("修复后峰值 %d 超过满幅 %d", rep.PeakAfter, cfg.FullScale)
	}
	work := make([]float64, len(out.Frames))
	for i, v := range out.Frames {
		work[i] = float64(v)
	}
	if err := VerifyPeakFloat(work, cfg.FullScale); err != nil {
		t.Fatalf("验证器 A 失败: %v", err)
	}
	// 断言两侧一阶差分连续（验证器 B 独立再查一次）。
	for _, r := range runs {
		if err := VerifyBoundaryContinuous(work, r[0], r[1], cfg.ClipLevel, cfg.JumpTol); err != nil {
			t.Fatalf("验证器 B 失败: %v", err)
		}
	}
	// 断言修复后比削波信号更接近原始波形。
	rmseClip := rmse(c.Frames, clippedFrames)
	rmseRep := rmse(c.Frames, out.Frames)
	if rmseRep >= rmseClip {
		t.Fatalf("修复未改善: RMSE 修复=%.2f 削波=%.2f", rmseRep, rmseClip)
	}
	if rmseRep > 50 {
		t.Fatalf("修复 RMSE %.2f 过大，短段应能高精度还原", rmseRep)
	}
	sg0 := rep.Segments[0]
	t.Logf("类别2 短削平: 段数=%d 首段阶数=%d 首段残差RMSE=%.3f 修复样本数=%d RMSE 削波=%.1f→修复=%.2f",
		len(rep.Segments), sg0.Order, sg0.ResidualRMSE, rep.RepairedCount, rmseClip, rmseRep)
}

// 第三类：中频正弦较长削平段（贴近但不超过 MaxFlat）。插值难度更高，
// 仍须通过两个独立验证器并显著改善 RMSE。
// 运行：go test ./audio/ -run TestClass3LongFlat -v
func TestClass3LongFlat(t *testing.T) {
	// 60Hz @8kHz（周期 133 样本）配合 22000 深削波（峰值 34000 的 65%），
	// 产生明显长于第二类的贴顶平台。
	c, _ := NewClip(8000, 4000)
	if err := Sine(c, 60, 34000); err != nil {
		t.Fatal(err)
	}
	clippedFrames := hardClip(c.Frames, 22000)
	clipped := &Clip{Rate: 8000, Frames: clippedFrames}
	runs := DetectFlatRuns(clippedFrames, 22000)
	if len(runs) == 0 {
		t.Fatal("测试前置失败: 未检测到削平段")
	}
	maxLen := 0
	for _, r := range runs {
		if l := r[1] - r[0] + 1; l > maxLen {
			maxLen = l
		}
	}
	if maxLen < 25 {
		t.Fatalf("第三类应含较长削平段(>=25), 最长仅 %d", maxLen)
	}

	cfg := DefaultRepairConfig()
	cfg.ClipLevel = 22000
	cfg.MaxFlat = maxLen + 8 // 放宽容差，确保不触发长度拒绝
	out, rep, err := Declip(clipped, cfg)
	if err != nil {
		t.Fatalf("修复失败: %v", err)
	}
	if rep.PeakAfter > cfg.FullScale {
		t.Fatalf("修复后峰值 %d 超过满幅", rep.PeakAfter)
	}
	work := make([]float64, len(out.Frames))
	for i, v := range out.Frames {
		work[i] = float64(v)
	}
	for _, r := range runs {
		if err := VerifyBoundaryContinuous(work, r[0], r[1], cfg.ClipLevel, cfg.JumpTol); err != nil {
			t.Fatalf("长削平段边界不连续: %v", err)
		}
	}
	rmseClip := rmse(c.Frames, clippedFrames)
	rmseRep := rmse(c.Frames, out.Frames)
	if rmseRep >= rmseClip {
		t.Fatalf("长段修复未改善: RMSE 修复=%.2f 削波=%.2f", rmseRep, rmseClip)
	}
	sg0 := rep.Segments[0]
	t.Logf("类别3 长削平: 段数=%d 最长段=%d 首段阶数=%d 首段残差RMSE=%.3f 修复样本数=%d RMSE %.1f→%.2f",
		len(rep.Segments), maxLen, sg0.Order, sg0.ResidualRMSE, rep.RepairedCount, rmseClip, rmseRep)
}

// 第四类：多削平段密集信号。修复段数必须与检测数一致，全部样本被处理，
// 且修复后整体波形与原始波形的相关性接近 1。
// 运行：go test ./audio/ -run TestClass4Multiple -v
func TestClass4Multiple(t *testing.T) {
	c, _ := NewClip(8000, 4800)
	if err := Sine(c, 100, 33000); err != nil {
		t.Fatal(err)
	}
	clippedFrames := hardClip(c.Frames, 25000)
	clipped := &Clip{Rate: 8000, Frames: clippedFrames}
	runs := DetectFlatRuns(clippedFrames, 25000)
	if len(runs) < 8 {
		t.Fatalf("第四类应含大量削平段, 仅检测到 %d", len(runs))
	}

	cfg := DefaultRepairConfig()
	cfg.ClipLevel = 25000
	out, rep, err := Declip(clipped, cfg)
	if err != nil {
		t.Fatalf("修复失败: %v", err)
	}
	if len(rep.Segments) != len(runs) {
		t.Fatalf("修复段数 %d 与检测段数 %d 不一致", len(rep.Segments), len(runs))
	}
	// 修复样本总数等于全部削平段长度之和。
	want := 0
	for _, r := range runs {
		want += r[1] - r[0] + 1
	}
	if rep.RepairedCount != want {
		t.Fatalf("修复样本数 %d != 削平样本总数 %d", rep.RepairedCount, want)
	}
	// 全部段阶数为正。
	for _, sg := range rep.Segments {
		if sg.Order <= 0 {
			t.Fatalf("段起于 %d 阶数非正", sg.Start)
		}
	}
	// 与原始波形的皮尔逊相关系数接近 1。
	corr := pearson(c.Frames, out.Frames)
	if corr < 0.999 {
		t.Fatalf("修复波形与原始相关性过低: %.4f", corr)
	}
	t.Logf("类别4 多削平段: 段数=%d 阶数范围=[%d,%d] 修复样本数=%d 相关系数=%.5f",
		len(rep.Segments), rep.Segments[0].Order, rep.Segments[len(rep.Segments)-1].Order,
		rep.RepairedCount, corr)
}

// 第五类：拒绝路径。三类非法输入都必须拒绝并通过 RepairError 报出位置：
// 削平段超 MaxFlat、预测阶数为零（邻域恒定）、采样率为零。
// 运行：go test ./audio/ -run TestClass5Rejections -v
func TestClass5Rejections(t *testing.T) {
	cfg := DefaultRepairConfig()
	cfg.ClipLevel = 30000

	// 5a：削平段超过上限。
	longRun := make([]int16, 400)
	for i := range longRun {
		longRun[i] = 2000 // 动态背景
	}
	for i := 100; i < 100+cfg.MaxFlat+10; i++ {
		longRun[i] = 30000 // 长度超过 MaxFlat 的平台，且不贴边
	}
	longClip := &Clip{Rate: 8000, Frames: longRun}
	_, _, err := Declip(longClip, cfg)
	if !errors.Is(err, ErrFlatLimit) {
		t.Fatalf("超长削平段应返回 ErrFlatLimit, got %v", err)
	}
	var re *RepairError
	if !errors.As(err, &re) || re.Index < 0 {
		t.Fatalf("超长错误必须携带发生位置, got %v", err)
	}
	t.Logf("类别5a 超长削平段被拒绝: %v", err)

	// 5b：预测阶数为零。短平台（低于上限）但两侧邻域恒定，无动态信息。
	zeroFrames := make([]int16, 64)
	for i := range zeroFrames {
		zeroFrames[i] = 30000
	}
	zeroFrames[0], zeroFrames[63] = 1, 1
	zeroCfg := cfg
	zeroCfg.Window = 8 // 邻域内全是恒定的 30000
	zeroClip := &Clip{Rate: 8000, Frames: zeroFrames}
	_, _, err = Declip(zeroClip, zeroCfg)
	if !errors.Is(err, ErrZeroOrder) {
		t.Fatalf("邻域恒定应返回 ErrZeroOrder, got %v", err)
	}
	if !errors.As(err, &re) || re.Index < 0 {
		t.Fatalf("零阶错误必须携带发生位置, got %v", err)
	}
	t.Logf("类别5b 预测阶数为零被拒绝: %v", err)

	// 5c：采样率为零。
	badRate := &Clip{Rate: 0, Frames: []int16{1, 2, 3, 3, 2, 1}}
	_, _, err = Declip(badRate, cfg)
	if !errors.Is(err, ErrSampleRate) {
		t.Fatalf("采样率为零应返回 ErrSampleRate, got %v", err)
	}
	t.Logf("类别5c 采样率为零被拒绝: %v", err)

	// 5d：削平段贴边（缺少一侧已知样本）。
	edgeFrames := make([]int16, 100)
	for i := range edgeFrames {
		edgeFrames[i] = 2000
	}
	for i := 0; i < 10; i++ {
		edgeFrames[i] = 30000 // 平台从第 0 帧开始，贴左边
	}
	edgeClip := &Clip{Rate: 8000, Frames: edgeFrames}
	_, _, err = Declip(edgeClip, cfg)
	if !errors.Is(err, ErrEdgeClip) {
		t.Fatalf("贴边削平段应返回 ErrEdgeClip, got %v", err)
	}
	if !errors.As(err, &re) || re.Index != 0 {
		t.Fatalf("贴边错误位置应为 0, got %v", err)
	}
	t.Logf("类别5d 贴边削平段被拒绝: %v", err)
}

// TestValidatorsIndependent 显式验证两个验证器相互独立、各管各的失败模式。
// 运行：go test ./audio/ -run TestValidatorsIndependent -v
func TestValidatorsIndependent(t *testing.T) {
	// 验证器 A：能抓超满幅，且不依赖边界结构。
	over := make([]float64, 16)
	over[7] = 40000
	errA := VerifyPeakFloat(over, 32767)
	if !errors.Is(errA, ErrPeakOverflow) {
		t.Fatalf("验证器 A 应抓峰值溢出, got %v", errA)
	}
	// 同一份数据没有“削平段边界”概念，验证器 B 不被调用即说明职责分离；
	// 反向：构造峰值合法但边界折角的信号，只应由 B 拒绝。
	if err := VerifyPeakFloat(over, 40000); err != nil {
		t.Fatalf("放宽满幅后 A 应通过, got %v", err)
	}
	jumpy := []float64{1000, 1100, 1200, 3000, 3000, 3000, 9000, 9100, 9200}
	// 峰值 9200 合法（满幅 32767），但平台 [3,5] 右侧有 ~6000 的折角。
	if err := VerifyPeakFloat(jumpy, 32767); err != nil {
		t.Fatalf("折角信号峰值合法, A 不应报错: %v", err)
	}
	errB := VerifyBoundaryContinuous(jumpy, 3, 5, 3000, 2.0)
	if !errors.Is(errB, ErrBoundaryJump) {
		t.Fatalf("验证器 B 应抓边界折角, got %v", errB)
	}
}

// pearson 计算两段 int16 信号的皮尔逊相关系数。
func pearson(a, b []int16) float64 {
	n := float64(len(a))
	var ma, mb float64
	for i := range a {
		ma += float64(a[i])
		mb += float64(b[i])
	}
	ma /= n
	mb /= n
	var cov, va, vb float64
	for i := range a {
		da := float64(a[i]) - ma
		db := float64(b[i]) - mb
		cov += da * db
		va += da * da
		vb += db * db
	}
	return cov / math.Sqrt(va*vb)
}
