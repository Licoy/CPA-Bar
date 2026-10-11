package view

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
)

// Task tracks one asynchronous operation started from a view: whether it
// runs, and how it ended. Keep tasks in page state, per instance, so a
// result can never land on another instance's page.
type Task struct {
	Running bool
	Err     error
	// Done is set when the task finished without error.
	Done bool
	At   time.Time
}

// Idle reports whether the task can start.
func (t *Task) Idle() bool { return !t.Running }

// Run starts fn on a goroutine unless the task is running, then sets the
// outcome and calls then (may be nil) on the main thread, through the
// window, which draws a new frame. timeout 0 leaves the client's own
// timeouts in charge.
func Run(win *mygo.Window, t *Task, timeout time.Duration, fn func(ctx context.Context) error, then func(err error)) {
	if t.Running {
		return
	}
	t.Running, t.Err, t.Done = true, nil, false
	go func() {
		ctx := context.Background()
		var cancel context.CancelFunc = func() {}
		if timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, timeout)
		}
		err := fn(ctx)
		cancel()
		update(win, func() {
			t.Running = false
			t.Err = err
			t.Done = err == nil
			t.At = time.Now()
			if then != nil {
				then(err)
			}
		})
	}()
}

// update runs fn on the main thread and redraws win; with no window (it
// was closed meanwhile) it still runs fn on the main thread.
func update(win *mygo.Window, fn func()) {
	if win != nil && !win.IsDestroyed() {
		win.Update(fn)
		return
	}
	runOnMain(fn)
}

// runOnMain hops to the main thread. Tests, which have no main loop,
// replace the hop with a queue they drain between frames (setRunOnMain).
func runOnMain(fn func()) {
	if hop := mainHop.Load(); hop != nil {
		(*hop)(fn)
		return
	}
	mygo.RunOnMain(fn)
}

// mainHop replaces mygo.RunOnMain when set. It is atomic: goroutines of a
// finished test may still hop while the next test swaps it.
var mainHop atomic.Pointer[func(func())]

// setRunOnMain makes runOnMain call hop, and returns what restores the
// previous one.
func setRunOnMain(hop func(func())) (restore func()) {
	old := mainHop.Swap(&hop)
	return func() { mainHop.Store(old) }
}

// OpenFile asks for one file, off the frame being built, then calls fn on
// the main thread with its path; a canceled dialog calls nothing.
func OpenFile(win *mygo.Window, title string, filters []mygo.FileFilter, fn func(path string)) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: liveWindow(win), Title: title, Filters: filters})
		if err != nil || len(paths) == 0 {
			return
		}
		update(win, func() { fn(paths[0]) })
	}()
}

// SaveFile asks where to save a file, then calls fn on the main thread
// with the path; a canceled dialog calls nothing.
func SaveFile(win *mygo.Window, title, name string, filters []mygo.FileFilter, fn func(path string)) {
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: liveWindow(win), Title: title, DefaultPath: name, Filters: filters, CreateDirectories: true})
		if err != nil || path == "" {
			return
		}
		update(win, func() { fn(path) })
	}()
}

func liveWindow(win *mygo.Window) *mygo.Window {
	if win == nil || win.IsDestroyed() {
		return nil
	}
	return win
}
