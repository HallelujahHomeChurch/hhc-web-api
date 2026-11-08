package bulletinwatermark

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PhaseTimer records one job's stage durations for a single structured log
// line. It deliberately stores only stage names, millisecond durations and
// bounded categorical attributes: never content, payloads, accounts, IDs or
// paths.
type PhaseTimer struct {
	mu     sync.Mutex
	now    func() time.Time
	start  time.Time
	last   time.Time
	names  []string
	values map[string]int64
	attrs  map[string]string
}

func NewPhaseTimer(start time.Time) *PhaseTimer {
	return newPhaseTimer(start, time.Now)
}

func newPhaseTimer(start time.Time, now func() time.Time) *PhaseTimer {
	return &PhaseTimer{now: now, start: start, last: start, values: map[string]int64{}, attrs: map[string]string{}}
}

// Mark records the elapsed time since the previous mark under name.
func (t *PhaseTimer) Mark(name string) {
	if t == nil {
		return
	}
	t.MarkAt(name, t.now())
}

// MarkAt records a phase that ended at an externally observed instant. The
// instant is clamped to the previous mark and the current time so clock skew
// between the helper and the worker never produces negative durations.
func (t *PhaseTimer) MarkAt(name string, at time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if now := t.now(); at.After(now) {
		at = now
	}
	if at.Before(t.last) {
		at = t.last
	}
	if _, exists := t.values[name]; !exists {
		t.names = append(t.names, name)
	}
	t.values[name] += at.Sub(t.last).Milliseconds()
	t.last = at
}

// Set records a bounded categorical attribute such as kind or outcome.
func (t *PhaseTimer) Set(key, value string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attrs[key] = value
}

// Attrs returns the log attributes in stable phase order.
func (t *PhaseTimer) Attrs() []any {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	keys := []string{"kind", "outcome", "error_class", "pages"}
	attrs := make([]any, 0, 2*(len(keys)+len(t.names)+1))
	for _, key := range keys {
		if value, ok := t.attrs[key]; ok {
			attrs = append(attrs, key, value)
		}
	}
	for _, name := range t.names {
		attrs = append(attrs, "ms_"+name, t.values[name])
	}
	return append(attrs, "ms_total", t.last.Sub(t.start).Milliseconds())
}

// Log writes exactly one line for the job.
func (t *PhaseTimer) Log(ctx context.Context) {
	if t == nil {
		return
	}
	slog.Default().InfoContext(context.WithoutCancel(ctx), "bulletin watermark job phases", t.Attrs()...)
}

type phaseTimerKey struct{}

func WithPhaseTimer(ctx context.Context, timer *PhaseTimer) context.Context {
	return context.WithValue(ctx, phaseTimerKey{}, timer)
}

func phaseTimerFrom(ctx context.Context) *PhaseTimer {
	timer, _ := ctx.Value(phaseTimerKey{}).(*PhaseTimer)
	return timer
}

func markPhase(ctx context.Context, name string) { phaseTimerFrom(ctx).Mark(name) }

func setPhaseAttr(ctx context.Context, key, value string) { phaseTimerFrom(ctx).Set(key, value) }

// helperPhaseOrder maps wall-clock markers written by the Python helper to
// worker phase names. The helper starts at the previous worker mark.
var helperPhaseOrder = [][2]string{{"ready", "helper_boot"}, {"model", "helper_model"}, {"encoded", "helper_encode"}, {"verified", "helper_verify"}}

// markHelperPhases reads the optional helper marker file. Missing or invalid
// markers are ignored: timing must never change rendering outcomes.
func markHelperPhases(ctx context.Context, dir string) {
	timer := phaseTimerFrom(ctx)
	if timer == nil {
		return
	}
	file, err := os.Open(filepath.Join(dir, "phases"))
	if err == nil {
		var marks map[string]float64
		data, readErr := io.ReadAll(io.LimitReader(file, 1<<10))
		file.Close()
		if readErr == nil && json.Unmarshal(data, &marks) == nil {
			for _, phase := range helperPhaseOrder {
				if value, ok := marks[phase[0]]; ok && value > 0 {
					timer.MarkAt(phase[1], time.Unix(0, int64(value*float64(time.Second))))
				}
			}
		}
	}
	timer.Mark("helper_exit")
}

// ProcessAge returns how long ago the current process started, based on
// Linux procfs. It returns false outside Linux or when procfs is unavailable.
// In the worker container the binary is PID 1, so this approximates the
// container start-to-main interval.
func ProcessAge() (time.Duration, bool) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	uptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	// Field 2 (comm) may contain spaces; parse after the closing parenthesis.
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(stat)[end+1:])
	// starttime is field 22 overall, i.e. index 19 after pid and comm.
	if len(fields) < 20 {
		return 0, false
	}
	ticks, err := strconv.ParseFloat(fields[19], 64)
	if err != nil {
		return 0, false
	}
	uptimeFields := strings.Fields(string(uptime))
	if len(uptimeFields) == 0 {
		return 0, false
	}
	up, err := strconv.ParseFloat(uptimeFields[0], 64)
	if err != nil {
		return 0, false
	}
	const clockTicks = 100 // USER_HZ on Linux container hosts.
	age := up - ticks/clockTicks
	if age < 0 {
		return 0, false
	}
	return time.Duration(age * float64(time.Second)), true
}

func phaseAttr(ctx context.Context, key string) (string, bool) {
	timer := phaseTimerFrom(ctx)
	if timer == nil {
		return "", false
	}
	timer.mu.Lock()
	defer timer.mu.Unlock()
	value, ok := timer.attrs[key]
	return value, ok
}
