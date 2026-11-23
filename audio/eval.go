package audio

// 端点检测的评测辅助：给出真实发声区间后，逐段核对
// "检测有声段覆盖全部真实有声" 与 "检测有声段不跨越两次发声之间的静音"，
// 并计算样本级漏检率。报告与测试共用这一份实现，避免两套口径。

import "math"

// SegmentStats 是一组区间的统计。
type SegmentStats struct {
	Count        int
	TotalSamples int
	MinSamples   int
	MaxSamples   int
	MeanSamples  int
}

// Stats 统计一组区间。空切片返回零值。
func Stats(spans []Segment) SegmentStats {
	if len(spans) == 0 {
		return SegmentStats{}
	}
	st := SegmentStats{Count: len(spans), MinSamples: math.MaxInt32}
	for _, s := range spans {
		d := s.Duration()
		st.TotalSamples += d
		if d < st.MinSamples {
			st.MinSamples = d
		}
		if d > st.MaxSamples {
			st.MaxSamples = d
		}
	}
	st.MeanSamples = st.TotalSamples / st.Count
	return st
}

// SpanCoverage 是一段真实发声被检测覆盖的情况。
type SpanCoverage struct {
	Truth        Segment
	Covered      int
	Missed       int
	FullyCovered bool
}

// CoverageCheck 是检测结果对一组真实发声区间的核对结果。
type CoverageCheck struct {
	PerSpan       []SpanCoverage
	TruthSamples  int
	MissedSamples int
	MissRate      float64 // 漏检样本 / 真实有声样本

	// FalseSamples 被判为有声、但不落在任何真实发声区间内的样本数。
	FalseSamples int
	// Crossings 是跨越了两次发声之间静音的检测段数。
	// 一个检测段只要同时覆盖了两段不同的真实发声，即记为一次跨越。
	Crossings int
	// ExtraSegments 是不覆盖任何真实发声的检测段数（纯误检段）。
	ExtraSegments int
}

// CheckCoverage 按样本逐一核对检测有声段与真实发声区间。
// truth 需按时间升序、互不重叠；检测结果按自身给定顺序处理。
//
// 判据：
//   - 覆盖：每个真实发声样本至少落在一个检测有声段内；
//   - 不跨越：任一检测有声段不得同时覆盖两段不同的真实发声。
func CheckCoverage(speech []Segment, truth []Segment) CoverageCheck {
	var out CoverageCheck
	// 每段真实发声被覆盖的样本数。
	out.PerSpan = make([]SpanCoverage, len(truth))
	for i, t := range truth {
		out.PerSpan[i].Truth = t
		out.TruthSamples += t.Duration()
	}

	// 用差分数组标记被检测覆盖的样本，避免逐段 O(n·m)。
	if out.TruthSamples > 0 {
		total := truth[len(truth)-1].End
		diff := make([]int, total+2)
		for _, s := range speech {
			a, b := s.Start, s.End
			if a < 0 {
				a = 0
			}
			if b > total {
				b = total
			}
			if b > a {
				diff[a]++
				diff[b]--
			}
		}
		run := 0
		cur := 0
		for i, t := range truth {
			start := t.Start
			// 推进到区间起点（差分前缀和与 i 同步）。
			for cur < start {
				run += diff[cur]
				cur++
			}
			cov := 0
			for cur < t.End {
				run += diff[cur]
				if run > 0 {
					cov++
				}
				cur++
			}
			out.PerSpan[i].Covered = cov
			out.PerSpan[i].Missed = t.Duration() - cov
			out.PerSpan[i].FullyCovered = cov == t.Duration()
			out.MissedSamples += t.Duration() - cov
		}
	}

	if out.TruthSamples > 0 {
		out.MissRate = float64(out.MissedSamples) / float64(out.TruthSamples)
	}

	// 跨越判定：检测段与真实发声的交集涉及几段。
	for _, s := range speech {
		hits := 0
		for _, t := range truth {
			if s.Start < t.End && s.End > t.Start {
				hits++
			}
		}
		if hits >= 2 {
			out.Crossings++
		}
		if hits == 0 {
			out.ExtraSegments++
		}
	}

	// 误检样本：检测有声但不在任何真实发声内。
	for _, s := range speech {
		in := 0
		for _, t := range truth {
			a := maxInt(s.Start, t.Start)
			b := minInt(s.End, t.End)
			if b > a {
				in += b - a
			}
		}
		if d := s.Duration() - in; d > 0 {
			out.FalseSamples += d
		}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
