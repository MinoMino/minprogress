package minprogress

import (
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProgressBoundsKnownTotal(t *testing.T) {
	pb := NewProgressBar(10)

	if got := pb.Progress(3); got != 3 {
		t.Fatalf("Progress(3) = %d, want 3", got)
	}

	if got := pb.Progress(100); got != 10 {
		t.Fatalf("Progress(100) = %d, want 10", got)
	}

	if got := pb.Progress(-100); got != 0 {
		t.Fatalf("Progress(-100) = %d, want 0", got)
	}
}

func TestProgressBoundsUnknownTotal(t *testing.T) {
	pb := NewProgressBar(UnknownTotal)

	if got := pb.Progress(4); got != 4 {
		t.Fatalf("Progress(4) = %d, want 4", got)
	}

	if got := pb.Progress(10); got != 14 {
		t.Fatalf("Progress(10) = %d, want 14", got)
	}

	if got := pb.Progress(-20); got != 0 {
		t.Fatalf("Progress(-20) = %d, want 0", got)
	}
}

func TestStringKnownTotal(t *testing.T) {
	pb := NewProgressBar(10)
	pb.Padding = 0
	pb.Width = 10
	pb.Progress(3)

	got := pb.String()
	want := " 30% ███░░░░░░░ (3/10)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestStringUnknownTotalWithUnits(t *testing.T) {
	pb := NewProgressBar(UnknownTotal)
	pb.Padding = 1
	pb.Unit = "item"
	pb.Units = "items"
	pb.Progress(1)

	got := pb.String()
	want := " 1 / ? item"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestAutoWidthUsesColumnsEnv(t *testing.T) {
	t.Setenv("COLUMNS", "80")

	pb := NewProgressBar(4)
	pb.Padding = 0
	pb.Width = AutoWidth
	pb.Progress(2)

	got := pb.String()
	if !strings.Contains(got, "██████████░░░░░░░░░░") {
		t.Fatalf("String() = %q, want a 20-char bar", got)
	}
}

func TestSpeedInfoAverageOnZeroValue(t *testing.T) {
	var s SpeedInfo
	if got := s.Average(); got != 0 {
		t.Fatalf("Average() = %f, want 0", got)
	}
}

func TestSpeedInfoBuffersWhenElapsedIsZero(t *testing.T) {
	now := time.Unix(0, 0)
	s := SpeedInfo{
		reportCount: 8,
		now: func() time.Time {
			return now
		},
	}

	s.Report(10)
	s.Report(10)
	now = now.Add(time.Second)
	s.Report(5)

	if got := s.Average(); math.Abs(got-15) > 1e-9 {
		t.Fatalf("Average() = %f, want 15", got)
	}
}

func TestReportUsesProgressBarReportCount(t *testing.T) {
	pb := NewProgressBar(10)
	pb.ReportCount = 7
	pb.Report(42, 1)

	pb.mu.RLock()
	si := pb.speeds[42]
	pb.mu.RUnlock()

	if si == nil {
		t.Fatal("expected speed info for uid 42")
	}

	if si.reportCount != 7 {
		t.Fatalf("reportCount = %d, want 7", si.reportCount)
	}

	if got := si.reports.Len(); got != 7 {
		t.Fatalf("ring len = %d, want 7", got)
	}
}

func TestSpeedFormatUsesConfiguredUnits(t *testing.T) {
	pb := NewProgressBar(UnknownTotal)
	pb.SpeedUnits = []Unit{{1000, "KX"}, {1, "X"}}

	pb.mu.Lock()
	pb.speeds[1] = &SpeedInfo{}
	pb.overallSpeed = 1500
	pb.mu.Unlock()

	got := pb.String()
	if !strings.Contains(got, "[1.5 KX/s]") {
		t.Fatalf("String() = %q, want custom speed units", got)
	}
}

func TestDoneRemovesSpeedFromOutput(t *testing.T) {
	pb := NewProgressBar(UnknownTotal)
	pb.SpeedUnits = DataUnits
	pb.ReportsPerSample = 1

	pb.Report(1, 10)
	if got := pb.String(); !strings.Contains(got, "/s]") {
		t.Fatalf("String() = %q, want speed in output", got)
	}

	pb.Done(1)
	if got := pb.String(); strings.Contains(got, "/s]") {
		t.Fatalf("String() = %q, did not expect speed in output", got)
	}
}

func TestReportHandlesZeroReportsPerSample(t *testing.T) {
	pb := NewProgressBar(UnknownTotal)
	pb.SpeedUnits = DataUnits
	pb.ReportsPerSample = 0

	pb.Report(1, 100)
	time.Sleep(10 * time.Millisecond)
	pb.Report(1, 100)

	if got := pb.AverageOverallSpeed(); got <= 0 {
		t.Fatalf("AverageOverallSpeed() = %f, want > 0", got)
	}
}

func TestAverageSpeedReturnsErrorForUnknownUID(t *testing.T) {
	pb := NewProgressBar(5)
	if _, err := pb.AverageSpeed(100); err == nil {
		t.Fatal("AverageSpeed(100) expected error, got nil")
	}
}

func TestConcurrentOperations(t *testing.T) {
	pb := NewProgressBar(1000)
	pb.SpeedUnits = DataUnits
	pb.ReportsPerSample = 1

	const workers = 6
	const iters = 400

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		uid := i

		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				pb.Report(uid, 1)
				if j%50 == 0 {
					pb.Done(uid)
				}
			}
			pb.Done(uid)
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_ = pb.String()
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < workers*iters; i++ {
			pb.Progress(1)
			pb.Progress(-1)
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent operations timed out")
	}
}
