package audio

import (
	"fmt"
	"math"
	"sort"
)

// BandReport 记录一次分频段增益处理的频段划分与各段能量。
type BandReport struct {
	Edges    []float64 // 频段边界频率（Hz），长度 Bands+1
	Energies []float64 // 各段增益前能量（单边谱）
	Gains    []float64 // 各段实际应用的线性增益
}

// BandGainConfig 分频段增益配置。
type BandGainConfig struct {
	Bands int       // 频段数，必须为正
	Gains []float64 // 各段线性增益，长度必须等于 Bands
	Edges []float64 // 可选：显式频段边界（Hz），长度 Bands+1，须覆盖 [0, Rate/2] 且严格递增；nil 时按能量拐点自动选取
}

// ApplyBandGain 对整段采样做分频段增益：先变换到频域按频点分解出各段能量，
// 再对每段应用可配增益，最后合成回时域。
// 合成前后总帧数不变；直流分量处理前移除、处理后加回，保证不变。
// 频段边界缺省按平滑对数能量谱的拐点（谷底）自动选取，不做固定切分。
func ApplyBandGain(c *Clip, cfg BandGainConfig) (*BandReport, error) {
	if c == nil {
		return nil, fmt.Errorf("ApplyBandGain: 参数 Clip 不能为 nil")
	}
	if c.Rate <= 0 {
		return nil, fmt.Errorf("ApplyBandGain: 参数 Rate 必须为正（当前 %d）", c.Rate)
	}
	if len(c.Frames) == 0 {
		return nil, fmt.Errorf("ApplyBandGain: 参数 Frames 不能为空")
	}
	if cfg.Bands <= 0 {
		return nil, fmt.Errorf("ApplyBandGain: 参数 Bands 必须为正（当前 %d）", cfg.Bands)
	}
	if len(cfg.Gains) != cfg.Bands {
		return nil, fmt.Errorf("ApplyBandGain: 参数 Gains 长度 %d 须等于 Bands %d", len(cfg.Gains), cfg.Bands)
	}

	n := len(c.Frames)
	half := n / 2

	// 去直流，合成后加回，保证直流分量不变。
	mean := 0.0
	for _, v := range c.Frames {
		mean += float64(v)
	}
	mean /= float64(n)

	// 精确 n 点 DFT（Bluestein），不做零填充，保证合成后总帧数与直流不变。
	re := make([]float64, n)
	im := make([]float64, n)
	for i, v := range c.Frames {
		re[i] = float64(v) - mean
	}
	dft(re, im, false)

	// 频段边界：bin 索引，半开区间 [eb[i], eb[i+1])，末段含奈奎斯特 bin。
	var eb []int
	if cfg.Edges != nil {
		var err error
		eb, err = explicitEdgeBins(cfg.Edges, cfg.Bands, c.Rate, n)
		if err != nil {
			return nil, err
		}
	} else {
		energy := make([]float64, half+1)
		for k := 1; k <= half; k++ {
			energy[k] = re[k]*re[k] + im[k]*im[k]
		}
		var err error
		eb, err = autoEdgeBins(energy, cfg.Bands)
		if err != nil {
			return nil, err
		}
	}

	// 各段增益前能量（单边谱）。
	energies := make([]float64, cfg.Bands)
	for k := 1; k <= half; k++ {
		energies[bandOf(eb, k)] += re[k]*re[k] + im[k]*im[k]
	}

	// 逐段应用增益（bin 0 直流不动；正负频率对称缩放）。
	for k := 1; k <= half; k++ {
		g := cfg.Gains[bandOf(eb, k)]
		re[k] *= g
		im[k] *= g
		if m := n - k; m != k {
			re[m] *= g
			im[m] *= g
		}
	}

	dft(re, im, true)
	for i := 0; i < n; i++ {
		v := math.Round(re[i] + mean)
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		c.Frames[i] = int16(v)
	}

	edgesHz := make([]float64, cfg.Bands+1)
	edgesHz[cfg.Bands] = float64(c.Rate) / 2
	for i := 1; i < cfg.Bands; i++ {
		edgesHz[i] = float64(eb[i]) * float64(c.Rate) / float64(n)
	}
	return &BandReport{
		Edges:    edgesHz,
		Energies: energies,
		Gains:    append([]float64(nil), cfg.Gains...),
	}, nil
}

// bandOf 返回 bin k 所属的频段下标。
func bandOf(eb []int, k int) int {
	i := 0
	for i+1 < len(eb) && k >= eb[i+1] {
		i++
	}
	return i
}

// autoEdgeBins 按能量拐点自动选取频段边界：
// 在平滑对数能量谱上找出显著峰（局部极大且不弱于最高峰 8 个对数单位），
// 相邻显著峰之间的能量谷底即为边界；显著峰不足时在最强峰两侧补谷，
// 仍不足则反复在最宽段内能量最低点补切。全程由能量分布驱动，不做固定切分。
func autoEdgeBins(energy []float64, bands int) ([]int, error) {
	half := len(energy) - 1
	if bands == 1 {
		return []int{1, half + 1}, nil
	}
	if bands > half {
		return nil, fmt.Errorf("ApplyBandGain: 参数 Bands %d 超过可用频点数 %d", bands, half)
	}

	const eps = 1e-12
	s := make([]float64, half+1) // 能量域 5 点平滑后取对数
	for k := 1; k <= half; k++ {
		lo, hi := k-2, k+2
		if lo < 1 {
			lo = 1
		}
		if hi > half {
			hi = half
		}
		sum := 0.0
		for j := lo; j <= hi; j++ {
			sum += energy[j]
		}
		s[k] = math.Log(sum/float64(hi-lo+1) + eps)
	}
	maxS := math.Inf(-1)
	for k := 1; k <= half; k++ {
		if s[k] > maxS {
			maxS = s[k]
		}
	}

	// 显著峰：局部极大（平台取左沿）且不弱于最高峰 8 个对数单位。
	type peak struct {
		pos int
		h   float64
	}
	var ps []peak
	for k := 1; k <= half; k++ {
		lo, hi := k-1, k+1
		if lo < 1 {
			lo = 1
		}
		if hi > half {
			hi = half
		}
		if s[k] >= s[lo] && s[k] >= s[hi] && (s[k] > s[lo] || s[k] > s[hi]) && s[k] >= maxS-8 {
			ps = append(ps, peak{k, s[k]})
		}
	}
	// 按高度降序贪心选取，间距至少 5 个 bin（抑制泄漏旁瓣），取前 bands 个。
	sort.Slice(ps, func(a, b int) bool {
		if ps[a].h != ps[b].h {
			return ps[a].h > ps[b].h
		}
		return ps[a].pos < ps[b].pos
	})
	var sel []int
	for _, p := range ps {
		if len(sel) == bands {
			break
		}
		ok := true
		for _, q := range sel {
			if d := p.pos - q; d < 5 && d > -5 {
				ok = false
				break
			}
		}
		if ok {
			sel = append(sel, p.pos)
		}
	}
	sort.Ints(sel)

	// 相邻显著峰之间的能量谷底作为边界。
	var cuts []int
	for i := 0; i+1 < len(sel); i++ {
		cuts = append(cuts, argminS(s, sel[i]+1, sel[i+1]-1, sel[i]+1))
	}

	// 显著峰不足时，在最强峰两侧补谷。
	if len(cuts) < bands-1 && len(sel) > 0 {
		sp := sel[0]
		for _, p := range sel {
			if s[p] > s[sp] {
				sp = p
			}
		}
		left := 1 // 最靠近最强峰的左侧边界
		for _, c := range cuts {
			if c < sp && c >= left {
				left = c
			}
		}
		if cut := argminS(s, left+1, sp-1, -1); cut > 0 {
			cuts = append(cuts, cut)
			sort.Ints(cuts)
		}
		if len(cuts) < bands-1 {
			right := half + 1 // 最靠近最强峰的右侧边界
			for _, c := range cuts {
				if c > sp && c < right {
					right = c
				}
			}
			hi := right - 1
			if hi > half {
				hi = half
			}
			if cut := argminS(s, sp+1, hi, -1); cut > 0 {
				cuts = append(cuts, cut)
				sort.Ints(cuts)
			}
		}
	}

	// 仍不足时，反复在最宽段内能量最低点补切。
	for len(cuts) < bands-1 {
		bounds := append(append([]int{1}, cuts...), half+1)
		widest, wpos := -1, -1
		for i := 0; i+1 < len(bounds); i++ {
			if bounds[i+1]-bounds[i] > widest {
				widest = bounds[i+1] - bounds[i]
				wpos = i
			}
		}
		if widest < 2 {
			break
		}
		lo, hi := bounds[wpos], bounds[wpos+1]
		cut := argminS(s, lo, hi-1, lo)
		if cut <= lo {
			cut = lo + 1
		}
		cuts = append(cuts, cut)
		sort.Ints(cuts)
	}

	eb := make([]int, bands+1)
	eb[0] = 1
	eb[bands] = half + 1
	copy(eb[1:bands], cuts)
	return eb, nil
}

// argminS 返回 s 在 [lo, hi] 上最小值的位置；区间为空时返回 fallback。
func argminS(s []float64, lo, hi, fallback int) int {
	if lo > hi {
		return fallback
	}
	pos, val := lo, math.Inf(1)
	for k := lo; k <= hi; k++ {
		if s[k] < val {
			val = s[k]
			pos = k
		}
	}
	return pos
}

// explicitEdgeBins 校验显式频段边界并换算为 bin 索引。
func explicitEdgeBins(edges []float64, bands, rate, n int) ([]int, error) {
	if len(edges) != bands+1 {
		return nil, fmt.Errorf("ApplyBandGain: 参数 Edges 长度 %d 须为 Bands+1=%d", len(edges), bands+1)
	}
	if edges[0] != 0 || edges[bands] != float64(rate)/2 {
		return nil, fmt.Errorf("ApplyBandGain: 参数 Edges 须覆盖 [0, Rate/2]（当前 [%v, %v]，Rate/2=%v）",
			edges[0], edges[bands], float64(rate)/2)
	}
	for i := 1; i <= bands; i++ {
		if edges[i] <= edges[i-1] {
			return nil, fmt.Errorf("ApplyBandGain: 参数 Edges 频段重叠或非递增（Edges[%d]=%v 未大于 Edges[%d]=%v）",
				i, edges[i], i-1, edges[i-1])
		}
	}
	half := n / 2
	eb := make([]int, bands+1)
	eb[0] = 1
	eb[bands] = half + 1
	for i := 1; i < bands; i++ {
		b := int(math.Round(edges[i] * float64(n) / float64(rate)))
		if b <= eb[i-1] {
			return nil, fmt.Errorf("ApplyBandGain: 参数 Edges 频段重叠（Edges[%d]=%v 与前一边界映射到同一频点）", i, edges[i])
		}
		if b > half {
			b = half
		}
		eb[i] = b
	}
	return eb, nil
}

// dft 任意长度离散傅里叶变换（Bluestein 算法），inverse 为 true 时做逆变换并归一化。
func dft(re, im []float64, inverse bool) {
	if !inverse {
		bluestein(re, im)
		return
	}
	// IDFT(x) = conj(DFT(conj(x))) / N
	for i := range im {
		im[i] = -im[i]
	}
	bluestein(re, im)
	n := float64(len(re))
	for i := range re {
		re[i] /= n
		im[i] = -im[i] / n
	}
}

// bluestein 用卷积-chirp 分解计算前向 DFT，支持任意长度。
func bluestein(re, im []float64) {
	n := len(re)
	m := 1
	for m < 2*n-1 {
		m <<= 1
	}
	ar := make([]float64, m)
	ai := make([]float64, m)
	br := make([]float64, m)
	bi := make([]float64, m)
	br[0] = 1
	for j := 0; j < n; j++ {
		j2 := (j * j) % (2 * n)
		wr, wi := math.Cos(math.Pi*float64(j2)/float64(n)), -math.Sin(math.Pi*float64(j2)/float64(n))
		ar[j] = re[j]*wr - im[j]*wi
		ai[j] = re[j]*wi + im[j]*wr
		if j > 0 {
			br[j] = wr
			bi[j] = -wi
			br[m-j] = wr
			bi[m-j] = -wi
		}
	}
	fft(ar, ai, false)
	fft(br, bi, false)
	for i := 0; i < m; i++ {
		ar[i], ai[i], br[i], bi[i] = ar[i]*br[i]-ai[i]*bi[i], ar[i]*bi[i]+ai[i]*br[i], 0, 0
	}
	fft(ar, ai, true)
	for k := 0; k < n; k++ {
		k2 := (k * k) % (2 * n)
		wr, wi := math.Cos(math.Pi*float64(k2)/float64(n)), -math.Sin(math.Pi*float64(k2)/float64(n))
		re[k] = ar[k]*wr - ai[k]*wi
		im[k] = ar[k]*wi + ai[k]*wr
	}
}

// fft 迭代基-2 快速傅里叶变换，inverse 为 true 时做逆变换并归一化。
func fft(re, im []float64, inverse bool) {
	n := len(re)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		ang := 2 * math.Pi / float64(size)
		if !inverse {
			ang = -ang
		}
		wr, wi := math.Cos(ang), math.Sin(ang)
		for i := 0; i < n; i += size {
			curR, curI := 1.0, 0.0
			for j := 0; j < size/2; j++ {
				uR, uI := re[i+j], im[i+j]
				vR := re[i+j+size/2]*curR - im[i+j+size/2]*curI
				vI := re[i+j+size/2]*curI + im[i+j+size/2]*curR
				re[i+j] = uR + vR
				im[i+j] = uI + vI
				re[i+j+size/2] = uR - vR
				im[i+j+size/2] = uI - vI
				curR, curI = curR*wr-curI*wi, curI*wr+curR*wi
			}
		}
	}
	if inverse {
		for i := range re {
			re[i] /= float64(n)
			im[i] /= float64(n)
		}
	}
}
