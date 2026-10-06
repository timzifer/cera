package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// worker is one engine process speaking the protocol in protocol.md.
type worker struct {
	eng     *engine
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     chan string
	timeout time.Duration
	dead    error // set once the process stopped answering
	pid     int   // the process doing the work (a launcher's child, for a Python venv)
}

var errUnsupported = errors.New("unsupported")

// start launches a worker of e; dir is the directory of the file it will
// open (WebAssembly workers see only that directory).
func (e *engine) start(dir string, timeout time.Duration) (*worker, error) {
	cmd := exec.Command(e.argv[0], e.argv[1:]...)
	cmd.Env = append(os.Environ(), e.env...)
	if e.wasmFS {
		cmd.Env = append(cmd.Env, "BENCH_WASM_ROOT="+dir)
	}
	cmd.Stderr = io.Discard
	if *verbose {
		cmd.Stderr = os.Stderr
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", e.name, err)
	}
	w := &worker{eng: e, cmd: cmd, in: in, out: make(chan string, 1), timeout: timeout, pid: cmd.Process.Pid}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			w.out <- sc.Text()
		}
		close(w.out)
	}()
	if r, err := w.call("pid"); err == nil {
		w.pid = int(parseInt(r[0]))
	} else if !errors.Is(err, errUnsupported) {
		return nil, err
	}
	return w, nil
}

// call sends one request and waits for its reply fields (without the
// leading "ok"). A worker that crashes or times out is killed and every
// later call fails.
func (w *worker) call(fields ...string) ([]string, error) {
	if w.dead != nil {
		return nil, w.dead
	}
	if _, err := io.WriteString(w.in, strings.Join(fields, "\t")+"\n"); err != nil {
		return nil, w.kill(fmt.Errorf("%s stopped: %v", w.eng.name, err))
	}
	select {
	case line, ok := <-w.out:
		if !ok {
			return nil, w.kill(fmt.Errorf("%s stopped (%s)", w.eng.name, fields[0]))
		}
		r := strings.Split(line, "\t")
		switch r[0] {
		case "ok":
			return r[1:], nil
		case "unsupported":
			return nil, errUnsupported
		case "err":
			if len(r) > 1 {
				return nil, errors.New(r[1])
			}
			return nil, errors.New("error")
		}
		return nil, w.kill(fmt.Errorf("%s: unexpected reply %q", w.eng.name, line))
	case <-time.After(w.timeout):
		return nil, w.kill(fmt.Errorf("%s: no reply after %v", w.eng.name, w.timeout))
	}
}

func (w *worker) kill(err error) error {
	w.dead = err
	w.cmd.Process.Kill()
	w.cmd.Wait()
	return err
}

// close asks the worker to quit and waits for it.
func (w *worker) close() {
	if w.dead != nil {
		return
	}
	io.WriteString(w.in, "quit\n")
	w.in.Close()
	done := make(chan struct{})
	go func() { w.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		w.cmd.Process.Kill()
		<-done
	}
	w.dead = errors.New("closed")
}

// peakRSS is the worker's peak resident memory in bytes so far, 0 if the
// platform does not say.
func (w *worker) peakRSS() uint64 {
	if w.dead != nil {
		return 0
	}
	return peakRSS(w.pid)
}

func parseInt(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
