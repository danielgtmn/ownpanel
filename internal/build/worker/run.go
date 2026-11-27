package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// recordingWriter remembers the first error of w, so a failed write of the
// stored source is told apart from a broken stream.
type recordingWriter struct {
	w   io.Writer
	err error
}

func (r *recordingWriter) Write(p []byte) (int, error) {
	n, err := r.w.Write(p)
	if err != nil && r.err == nil {
		r.err = err
	}
	return n, err
}

// Source receives the source tree of a build: it is checked as it arrives
// (names, types, sizes) and kept as it came until the build starts, in the
// waiting area of the build disk, whose quota bounds what all waiting
// builds hold together.
func (w *Worker) Source(ctx context.Context, id uint64, body io.Reader, strip int) (build.Build, error) {
	if strip < 0 || strip > maxStrip {
		return build.Build{}, errorf(statusBadRequest, "strip must be between 0 and %d", maxStrip)
	}
	w.mu.Lock()
	j, err := w.get(id)
	if err == nil && (j.state != build.StateCreated || j.hasSource || j.receiving) {
		err = errorf(statusConflict, "build %d has its source tree already", id)
	}
	if err != nil {
		w.mu.Unlock()
		return build.Build{}, err
	}
	j.receiving = true
	w.mu.Unlock()

	ok := false
	defer func() {
		w.mu.Lock()
		j.receiving = false
		w.mu.Unlock()
		if !ok {
			os.Remove(w.sourcePath(id))
		}
	}()

	f, err := os.OpenFile(w.sourcePath(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return build.Build{}, err
	}
	defer f.Close()
	limited := &limitedReader{r: body, left: build.MaxSourceStreamBytes, err: errSourceStream}
	store := &recordingWriter{w: f}
	tee := io.TeeReader(limited, store)
	err = unpackSource(ctx, "", tee, strip, defaultSourceLimits)
	if err == nil {
		// What follows the archive (padding) is kept too, so the stored
		// stream reads exactly as it was checked.
		_, err = io.Copy(io.Discard, tee)
	}
	if err == nil {
		err = f.Sync()
	}
	switch {
	case store.err != nil && isDiskFull(store.err):
		return build.Build{}, errorf(statusNoSpace, "there is no room for more waiting builds on the build disk; try again when builds have ended")
	case store.err != nil:
		return build.Build{}, store.err
	case errors.Is(err, errSourceStream):
		return build.Build{}, errorf(statusTooLarge, "the source tree is larger than %d MB", build.MaxSourceBytes>>20)
	case isTreeError(err):
		return build.Build{}, errorf(statusBadRequest, "%v", err)
	case err != nil:
		return build.Build{}, err
	}
	ok = true
	w.mu.Lock()
	defer w.mu.Unlock()
	j.hasSource, j.strip = true, strip
	return j.view(), nil
}

var errSourceStream = errors.New("the source stream is too large")

// outcome is how a run ended.
type outcome struct {
	state, reason, message string
	exitCode               int
}

func failed(reason, format string, args ...any) outcome {
	return outcome{state: build.StateFailed, reason: reason, message: fmt.Sprintf(format, args...)}
}

// run runs one build in its slot and finishes it. Whatever happens, the
// slot is empty again afterwards.
func (w *Worker) run(ctx context.Context, j *job) {
	id := j.spec.ID
	logf, err := newLogFile(w.logPath(id), j.limits.LogBytes)
	if err != nil {
		w.log.Error("build log", "build", id, "err", err)
		w.finish(j, build.StateFailed, build.ReasonInternal, "the build could not be started", 0)
		return
	}
	w.mu.Lock()
	j.log = logf
	w.mu.Unlock()

	out := w.execute(ctx, j, logf)

	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := w.resetSlot(cleanup, j.slot); err != nil {
		// No build starts until the worker is restarted (which empties
		// every slot first): a build must never find another one's files.
		w.log.Error("cleaning the slot after a build", "build", id, "slot", j.slot, "err", err)
		w.mu.Lock()
		w.broken = "the folder of a finished build could not be removed"
		w.mu.Unlock()
		w.setDown(unavailable(build.UnavailableDisk, "the folder of a finished build could not be removed: %v", err))
	}
	os.Remove(w.sourcePath(id))
	if out.message != "" && out.state != build.StateSucceeded {
		logf.Note(out.message)
	}
	logf.Close()
	w.finish(j, out.state, out.reason, out.message, out.exitCode)
}

// execute does the work of run.
func (w *Worker) execute(ctx context.Context, j *job, logf *logFile) outcome {
	spec := j.spec
	if p := w.protected(); p != nil {
		w.log.Error("build not run: the limits for builds are gone", "build", spec.ID, "reason", p.Reason, "detail", p.Detail)
		w.setDown(p)
		return failed(build.ReasonUnprotected, "the build was not run: the limits that keep builds away from this server are not in place")
	}
	resolvers, err := w.resolvers()
	if err != nil {
		return failed(build.ReasonUnprotected, "the build was not run: the name servers for builds are not known")
	}
	img, ok := build.FindImage(w.cfg.Images, spec.Image)
	if !ok {
		return failed(build.ReasonInternal, "the build image is not available")
	}
	if err := w.makeRoom(ctx, w.roomNeeded(j.slot), spec.Site); err != nil {
		return failed(build.ReasonDisk, "the build was not run: the build disk is full")
	}
	slot := w.slotDir(j.slot)
	ws, ctl := filepath.Join(slot, "workspace"), filepath.Join(slot, "control")
	dirs := []string{ws, ctl}
	var cache string
	if spec.Untrusted {
		// A cache of its own in the slot (the slot's quota, emptied with
		// it), never the site's.
		cache = filepath.Join(slot, "cache")
		dirs = append(dirs, cache)
	} else {
		cache, err = w.cacheFor(ctx, spec.Site)
		if err != nil {
			w.log.Error("build cache", "build", spec.ID, "err", err)
			return failed(build.ReasonInternal, "the build cache could not be prepared")
		}
	}
	for _, d := range dirs {
		if err := os.Mkdir(d, 0o700); err != nil {
			w.log.Error("build folders", "build", spec.ID, "err", err)
			return failed(build.ReasonInternal, "the build folder could not be prepared")
		}
	}
	src, err := os.Open(w.sourcePath(spec.ID))
	if err != nil {
		return failed(build.ReasonInternal, "the source tree is gone")
	}
	err = unpackSource(ctx, ws, src, j.strip, defaultSourceLimits)
	src.Close()
	os.Remove(w.sourcePath(spec.ID))
	switch {
	case ctx.Err() != nil:
		return w.killed(ctx, j, false)
	case isTreeError(err):
		reason := build.ReasonSource
		if errors.Is(err, errNoRoom) {
			reason = build.ReasonDisk
		}
		return failed(reason, "%v", err)
	case err != nil:
		w.log.Error("unpacking the source", "build", spec.ID, "err", err)
		return failed(build.ReasonInternal, "the source tree could not be unpacked")
	}
	if err := writeControl(ctl, spec); err != nil {
		w.log.Error("control files", "build", spec.ID, "err", err)
		return failed(build.ReasonInternal, "the build could not be prepared")
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(j.limits.Seconds)*time.Second)
	defer cancel()
	go w.watch(runCtx, j, cancel)

	mask := newMasker(logf, spec.Secrets)
	res, err := w.cfg.Runtime.Run(runCtx, Run{
		ID: spec.ID, Image: img, Workspace: ws, Cache: cache, Control: ctl,
		Limits: j.limits, Resolvers: resolvers, Output: mask,
	})
	_ = mask.Flush()
	// The secrets are not needed on disk any longer.
	_ = os.Remove(filepath.Join(ctl, controlEnv))
	if err != nil {
		w.log.Error("running the build", "build", spec.ID, "err", err)
		return failed(build.ReasonInternal, "the build could not be run")
	}
	if res.Killed {
		return w.killed(runCtx, j, true)
	}
	if res.OutOfMemory {
		return failed(build.ReasonMemory, "the build used more than its %d MB of memory and was stopped", j.limits.MemoryBytes>>20)
	}
	if res.ExitCode != 0 {
		if w.full(ws) || w.full(cache) {
			return outcome{state: build.StateFailed, reason: build.ReasonDisk, exitCode: res.ExitCode,
				message: fmt.Sprintf("the build ran out of disk space (%d MB for a build, %d MB for the cache)", w.cfg.WorkspaceBytes>>20, w.cfg.CacheBytes>>20)}
		}
		return outcome{state: build.StateFailed, reason: build.ReasonExit, exitCode: res.ExitCode,
			message: fmt.Sprintf("the build command failed with exit code %d", res.ExitCode)}
	}
	return w.collect(ctx, j, ws)
}

// roomNeeded is the free space the build disk needs before a build in slot
// starts: the whole quota of this build, plus what the builds running in
// other slots may still write.
func (w *Worker) roomNeeded(slot int) int64 {
	need := w.cfg.WorkspaceBytes
	w.mu.Lock()
	var others []int
	for n, j := range w.slots {
		if j != nil && n != slot {
			others = append(others, n)
		}
	}
	w.mu.Unlock()
	for _, n := range others {
		if u, err := w.cfg.Disk.Usage(w.slotDir(n)); err == nil {
			need += max(0, w.cfg.WorkspaceBytes-u.UsedBytes())
		}
	}
	return need
}

// full: the project of dir has (almost) no space or files left.
func (w *Worker) full(dir string) bool {
	u, err := w.cfg.Disk.Usage(dir)
	if err != nil {
		return false
	}
	return u.FreeBytes < 1<<20 || u.FreeFiles < 16
}

// killed is the outcome of a build whose context ended.
func (w *Worker) killed(ctx context.Context, j *job, ran bool) outcome {
	w.mu.Lock()
	cancelled, why, message := j.cancelled, j.why, j.message
	w.mu.Unlock()
	switch {
	case why == build.ReasonUnprotected:
		return failed(build.ReasonUnprotected, "the build was stopped: the limits that keep builds away from this server disappeared while it ran")
	case cancelled:
		return outcome{state: build.StateCancelled, message: message}
	case ran && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return failed(build.ReasonTimeout, "the build ran longer than %s and was stopped", time.Duration(j.limits.Seconds)*time.Second)
	default:
		return outcome{state: build.StateCancelled, message: "the build service stopped"}
	}
}

// watch stops the build when the network limits or the build disk
// disappear while it runs.
func (w *Worker) watch(ctx context.Context, j *job, stop context.CancelFunc) {
	t := time.NewTicker(w.cfg.WatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if p := w.protected(); p != nil {
				w.log.Error("stopping a build: the limits for builds are gone", "build", j.spec.ID, "reason", p.Reason, "detail", p.Detail)
				w.mu.Lock()
				j.why = build.ReasonUnprotected
				w.mu.Unlock()
				w.setDown(p)
				stop()
				return
			}
		}
	}
}

// collect packs the output folder of a successful build into the results.
func (w *Worker) collect(ctx context.Context, j *job, ws string) outcome {
	id := j.spec.ID
	f, err := os.OpenFile(w.outputPath(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		w.log.Error("output file", "build", id, "err", err)
		return failed(build.ReasonInternal, "the output could not be stored")
	}
	size, files, err := packOutput(ctx, ws, j.spec.Output, f, defaultOutputLimits)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(w.outputPath(id))
		switch {
		case isTreeError(err):
			return failed(build.ReasonOutput, "%v", err)
		case isDiskFull(err):
			return failed(build.ReasonInternal, "there is no room to keep the output; fetch or remove finished builds")
		default:
			w.log.Error("packing the output", "build", id, "err", err)
			return failed(build.ReasonInternal, "the output could not be packed")
		}
	}
	w.mu.Lock()
	j.outBytes, j.outFiles = size, files
	w.mu.Unlock()
	return outcome{state: build.StateSucceeded}
}

// Log writes the log of a build from offset on to out (calling flush after
// each piece). With follow it keeps going until the build has ended or ctx
// is done.
func (w *Worker) Log(ctx context.Context, id uint64, offset int64, follow bool, out io.Writer, flush func()) error {
	if offset < 0 {
		return errorf(statusBadRequest, "the offset must not be negative")
	}
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for {
		w.mu.Lock()
		j, err := w.get(id)
		var logf *logFile
		var done <-chan struct{}
		finished := false
		if err == nil {
			logf, done, finished = j.log, j.done, build.Finished(j.state)
		}
		w.mu.Unlock()
		if err != nil {
			return err
		}
		if f == nil && (logf != nil || finished) {
			f, err = os.Open(w.logPath(id))
			if errors.Is(err, os.ErrNotExist) {
				return nil // ended before it ran: no log
			}
			if err != nil {
				return err
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				return err
			}
		}
		var changed <-chan struct{}
		closed := finished
		if logf != nil {
			_, _, closed, changed = logf.state()
		}
		if f != nil {
			if _, err := io.Copy(out, f); err != nil {
				return err
			}
			flush()
		}
		if !follow || closed {
			if f != nil && closed {
				// What arrived between the copy and the end.
				if _, err := io.Copy(out, f); err != nil {
					return err
				}
				flush()
			}
			return nil
		}
		if changed == nil {
			// Waiting for a slot: look again in a moment.
			t := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil
			case <-done:
				t.Stop()
			case <-t.C:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		}
	}
}

// Output opens the output of a successful build.
func (w *Worker) Output(id uint64) (*os.File, error) {
	w.mu.Lock()
	j, err := w.get(id)
	if err == nil && j.state != build.StateSucceeded {
		err = errorf(statusConflict, "build %d has no output (it is %s)", id, j.state)
	}
	w.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return os.Open(w.outputPath(id))
}
