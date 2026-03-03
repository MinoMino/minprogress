// A progress bar for arbitrary units, which can also keep track of
// the speed of progress in terms of units/second. Safe for concurrent use.
//
// To keep track of speeds, have each "progress maker" report how many
// units of progress it has done at desired intervals using Report()
// with a unique identifier. If such a progress maker has finished its
// task, have it report it with Done(). For every X amount of reports,
// it will sample the average speed of all the progress makers and include
// it as part of the formatted progress bar string.
package minprogress

import (
	"container/ring"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	AutoWidth          = 0
	UnknownTotal       = 0
	defaultReportCount = 50
	defaultBarWidth    = 30
	defaultSampleEvery = 15
)

type Unit struct {
	Size int64
	Name string
}

// Data speed units. Useful for transfer speeds.
var DataUnits = []Unit{
	{1 << 40, "TiB"},
	{1 << 30, "GiB"},
	{1 << 20, "MiB"},
	{1 << 10, "KiB"},
	{1, "B"},
}

// Holds info about the speed of progress. Provides
// methods to get info and to report progress.
// Can be used uninitialized, using the default number of
// 50 reports.
type SpeedInfo struct {
	reports          *ring.Ring
	last             time.Time
	reportCount, buf int
	init             bool
	now              func() time.Time
}

func (s *SpeedInfo) clockNow() time.Time {
	if s.now != nil {
		return s.now()
	}

	return time.Now()
}

func (s *SpeedInfo) ensureReports() {
	if s.reports != nil {
		return
	}

	if s.reportCount <= 0 {
		s.reportCount = defaultReportCount
	}

	s.reports = ring.New(s.reportCount)
}

// Report n amount of progress made since last call.
func (s *SpeedInfo) Report(n int) {
	s.ensureReports()
	now := s.clockNow()

	if !s.init {
		s.init = true
		s.last = now
		return
	}

	elapsed := now.Sub(s.last)
	if elapsed <= 0 {
		// If the call since last time is too fast, elapsed might evaluate
		// to 0, so buffer n and include it on the next non-zero sample.
		s.buf += n
		return
	}

	total := n + s.buf
	s.buf = 0
	s.reports.Value = float64(total) / elapsed.Seconds()
	s.reports = s.reports.Next()
	s.last = now
}

// Get the average speed.
func (s *SpeedInfo) Average() float64 {
	if s.reports == nil {
		return 0
	}

	sum := 0.0
	i := 0
	s.reports.Do(func(rep interface{}) {
		if rep == nil {
			return
		}

		sum += rep.(float64)
		i++
	})

	if i == 0 {
		return 0
	}

	return sum / float64(i)
}

type ProgressBar struct {
	// The characters used for the empty and full parts of the bar itself.
	// Uses ░ and █ by default, respectively.
	Empty, Full rune
	// If desired, they can be set to words describing what a unit of progress
	// means. For instance "File" and "Files" if the bar represents how many files
	// have been processed.
	Unit, Units string
	// The speed units used for reporting speed. See DataUnits for an example
	// of units for data transfer/processing.
	SpeedUnits []Unit
	// The width of the console and the padding on the left of the bar.
	// The width is AutoWidth by default, which will automatically determine
	// the appropriate size depending on the terminal size.
	Width, Padding int
	// Number of reports that should be stored to calculate the average.
	// The higher it is, the less volatile it is. Default is 50.
	ReportCount, OverallReportCount int
	// How many calls to Report() it should take for it to sample all
	// speed and calculate an overall average speed. Default is 15.
	ReportsPerSample int

	mu                  sync.RWMutex
	reports             int
	overallSpeed        float64
	overallSpeedSamples *ring.Ring
	// A map holding speed information for each individual ID.
	speeds         map[int]*SpeedInfo
	current, total int
}

// Creates a new progress bar starting at 0 units. If total is set
// to UnknownTotal, no bar will be displayed, but it will still display
// the number of units of progress that have been made and whatnot.
func NewProgressBar(total int) *ProgressBar {
	if total < 0 {
		total = 0
	}
	return &ProgressBar{
		Empty:              '░',
		Full:               '█',
		total:              total,
		Padding:            2,
		speeds:             make(map[int]*SpeedInfo),
		ReportCount:        defaultReportCount,
		OverallReportCount: defaultReportCount,
		ReportsPerSample:   defaultSampleEvery,
	}
}

// Make n amount of units in progress.
func (p *ProgressBar) Progress(n int) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.total == UnknownTotal {
		p.current = max(0, p.current+n)
	} else {
		p.current = max(0, min(p.total, p.current+n))
	}
	return p.current
}

// Report how many units of progress have been made since last call
// for that particular UID.
func (p *ProgressBar) Report(uid, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	si, ok := p.speeds[uid]
	if !ok {
		si = &SpeedInfo{reportCount: p.ReportCount}
		p.speeds[uid] = si
	}

	si.Report(n)
	p.reports++

	// Sample sum of averages if we need to.
	sampleEvery := p.ReportsPerSample
	if sampleEvery <= 0 {
		sampleEvery = 1
	}

	if p.reports%sampleEvery == 0 {
		p.sampleOverallSpeedLocked()
	}
}

func (p *ProgressBar) sampleOverallSpeedLocked() {
	totalSpeed := 0.0
	for _, si := range p.speeds {
		totalSpeed += si.Average()
	}

	if p.overallSpeedSamples == nil {
		count := p.OverallReportCount
		if count <= 0 {
			count = defaultReportCount
		}

		p.overallSpeedSamples = ring.New(count)
	}

	p.overallSpeedSamples.Value = totalSpeed
	p.overallSpeedSamples = p.overallSpeedSamples.Next()

	sum := 0.0
	samples := 0
	p.overallSpeedSamples.Do(func(rep interface{}) {
		if rep == nil {
			return
		}

		sum += rep.(float64)
		samples++
	})

	if samples == 0 {
		p.overallSpeed = 0
		return
	}

	p.overallSpeed = sum / float64(samples)
}

// Report that the progress of a UID is done. This is important
// to call to keep an accurate overall average.
func (p *ProgressBar) Done(uid int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.speeds, uid)
	if len(p.speeds) == 0 {
		p.overallSpeed = 0
		p.overallSpeedSamples = nil
		return
	}

	p.sampleOverallSpeedLocked()
}

// Returns the average speed of a particular UID. Returns an error
// if and only if the UID doesn't exist.
func (p *ProgressBar) AverageSpeed(uid int) (float64, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if si, ok := p.speeds[uid]; ok {
		return si.Average(), nil
	}

	return 0, fmt.Errorf("nonexistent UID: %d", uid)
}

// Returns the average cumulative speed and the number of UID used to
// calculate it.
func (p *ProgressBar) AverageOverallSpeed() (avg float64) {
	p.mu.RLock()
	avg = p.overallSpeed
	p.mu.RUnlock()
	return
}

// Gets the whole formatted progress bar.
func (p *ProgressBar) String() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var units, out string
	if p.Unit != "" && p.Units != "" {
		if p.current == 1 {
			units = " " + p.Unit
		} else {
			units = " " + p.Units
		}
	}

	if p.total == UnknownTotal {
		out = fmt.Sprintf("%s%d / ?%s%s",
			strings.Repeat(" ", max(0, p.Padding)), p.current, units, p.speedFormatLocked())
	} else {
		percentage := 0
		if p.total > 0 {
			percentage = int(100 * float64(p.current) / float64(p.total))
		}

		out = fmt.Sprintf("%s%3d%% %s (%d/%d)%s%s",
			strings.Repeat(" ", max(0, p.Padding)), percentage, p.barLocked(),
			p.current, p.total, units, p.speedFormatLocked())
	}

	return out
}

func (p *ProgressBar) barLocked() string {
	if p.total <= 0 {
		return ""
	}

	ratio := float64(p.current) / float64(p.total)
	width := p.Width
	if width == AutoWidth {
		cols, err := strconv.Atoi(os.Getenv("COLUMNS"))
		if err == nil && cols > 0 {
			width = int((float64(cols) / 4) + 0.5)
		}
	}

	if width <= 0 {
		width = defaultBarWidth
	}

	fulls := int((float64(width) * ratio) + 0.5)
	fulls = min(width, max(0, fulls))

	return strings.Repeat(string(p.Full), fulls) + strings.Repeat(string(p.Empty), width-fulls)
}

func (p *ProgressBar) speedFormatLocked() string {
	if p.SpeedUnits == nil || len(p.SpeedUnits) == 0 {
		return ""
	}

	if len(p.speeds) == 0 {
		return ""
	}

	avg := p.overallSpeed
	unit := p.SpeedUnits[len(p.SpeedUnits)-1]
	for _, u := range p.SpeedUnits {
		if u.Size <= 0 {
			continue
		}

		if avg >= float64(u.Size) {
			unit = u
			break
		}
	}

	divisor := float64(unit.Size)
	if divisor <= 0 {
		divisor = 1
	}

	return fmt.Sprintf(" [%3.1f %s/s]", avg/divisor, unit.Name)
}

func min(x, y int) int {
	if x < y {
		return x
	}

	return y
}

func max(x, y int) int {
	if x > y {
		return x
	}

	return y
}
