package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/timzifer/cera/bench/internal/clock"
)

// pageResult is what was measured on one page. The times stay in memory:
// the report turns them into ratios.
type pageResult struct {
	page   int
	times  map[string][]int64 // per engine, one per run, in run order
	ink    map[string]float64
	size   map[string][2]int
	err    map[string]string
	reps   map[string]int // renders per timed sample
	again  []float64      // cera: render again from the display list / first render
	allocs int64          // cera, per render
	bytes  int64
}

// fileResult is what was measured on one file.
type fileResult struct {
	f     *file
	pages int
	// first is opening the file and drawing its first page in a fresh
	// process, the wait before a viewer shows something.
	first map[string]int64
	// all is drawing every page on all cores, one per run; one is the
	// same on one core, each timed right before or after the run of all
	// with the same index, so that the gain is a paired ratio.
	all     map[string][]int64
	one     map[string][]int64
	allErr  map[string]string
	pooled  map[string]bool // all cores as one process per core
	rss     map[string]uint64
	openErr map[string]string
	results []*pageResult
}

// slot is one engine's worker on the file being measured, restarted when
// it crashes (up to a budget), so one bad page costs only that page.
type slot struct {
	e        *engine
	f        *file
	w        *worker
	restarts int
	err      error // no worker any more
	rss      uint64
}

const maxRestarts = 3

// minSample is the least time one timed sample spans; maxReps bounds the
// renders it takes.
const (
	minSample = 20_000_000 // ns
	maxReps   = 50
)

func (s *slot) path() string {
	if s.e.wasmFS {
		return "/" + filepath.Base(s.f.path)
	}
	return s.f.path
}

// open starts a worker and opens the file; it returns the time opening took.
func (s *slot) open() (int64, int, error) {
	if s.w != nil {
		s.rss = max(s.rss, s.w.peakRSS())
		s.w.close()
	}
	w, err := s.e.start(filepath.Dir(s.f.path), *timeout)
	if err != nil {
		return 0, 0, err
	}
	s.w = w
	args := []string{"open", s.path()}
	if s.f.password != "" {
		args = append(args, s.f.password)
	}
	r, err := w.call(args...)
	if err != nil {
		return 0, 0, err
	}
	n, _ := strconv.Atoi(r[1])
	return parseInt(r[0]), n, nil
}

// get returns a live worker, restarting a crashed one.
func (s *slot) get() (*worker, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.w.dead == nil {
		return s.w, nil
	}
	if s.restarts >= maxRestarts {
		s.err = fmt.Errorf("gave up after %d restarts: %v", maxRestarts, s.w.dead)
		return nil, s.err
	}
	s.restarts++
	if _, _, err := s.open(); err != nil {
		s.err = err
		return nil, err
	}
	return s.w, nil
}

func (s *slot) close() {
	if s.w != nil {
		s.rss = max(s.rss, s.w.peakRSS())
		s.w.close()
	}
}

// render draws page i; it returns the time and the reply.
func (s *slot) render(i int, scale string, ink bool) (int64, []string, error) {
	w, err := s.get()
	if err != nil {
		return 0, nil, err
	}
	args := []string{"render", strconv.Itoa(i), scale}
	if ink {
		args = append(args, "1")
	}
	r, err := w.call(args...)
	if err != nil {
		return 0, nil, err
	}
	return parseInt(r[0]), r, nil
}

// rotate returns the engines starting at the k-th, so that each engine
// takes every position in turn.
func rotate[T any](s []T, k int) []T {
	if len(s) == 0 {
		return s
	}
	k %= len(s)
	return append(append([]T(nil), s[k:]...), s[:k]...)
}

// measureFile measures every engine on f. The engines take turns page by
// page and run by run, so a change of load on the machine falls on all of
// them alike.
func measureFile(f *file, engines []*engine) *fileResult {
	fr := &fileResult{f: f, first: map[string]int64{}, all: map[string][]int64{}, one: map[string][]int64{}, allErr: map[string]string{},
		pooled: map[string]bool{}, rss: map[string]uint64{}, openErr: map[string]string{}}
	scale := strconv.FormatFloat(*dpi/72, 'g', -1, 64)

	// Open in a fresh process and draw the first page: cold.
	var slots []*slot
	coldPage0 := map[string][]string{}
	for _, e := range engines {
		s := &slot{e: e, f: f}
		ns, n, err := s.open()
		if err != nil {
			fr.openErr[e.name] = err.Error()
			s.close()
			continue
		}
		if fr.pages == 0 || e.name == "cera" {
			fr.pages = n
		}
		if t, r, err := s.render(0, scale, true); err == nil {
			fr.first[e.name] = ns + t
			coldPage0[e.name] = r
		}
		slots = append(slots, s)
	}
	defer func() {
		for _, s := range slots {
			s.close()
		}
	}()
	if *maxPages > 0 {
		fr.pages = min(fr.pages, *maxPages)
	}
	for i := range fr.pages {
		fr.results = append(fr.results, &pageResult{page: i, times: map[string][]int64{}, ink: map[string]float64{},
			size: map[string][2]int{}, err: map[string]string{}, reps: map[string]int{}})
	}
	if fr.pages == 0 {
		return fr
	}
	for name, e := range fr.openErr {
		for _, pr := range fr.results {
			pr.err[name] = e
		}
	}

	// An untimed pass: each page once, which warms up caches and JITs and
	// finds the pages an engine cannot draw.
	for i, pr := range fr.results {
		for _, s := range rotate(slots, i) {
			if s.err != nil {
				pr.err[s.e.name] = s.err.Error()
				continue
			}
			_, r, err := s.render(i, scale, i != 0)
			if i == 0 && err == nil {
				r = coldPage0[s.e.name]
				if r == nil {
					_, r, err = s.render(0, scale, true)
				}
			}
			if err != nil {
				pr.err[s.e.name] = err.Error()
				continue
			}
			pr.size[s.e.name] = [2]int{int(parseInt(r[1])), int(parseInt(r[2]))}
			if len(r) > 3 {
				pr.ink[s.e.name] = parseFloat(r[3])
			}
			// cera's allocations, right after the page's first warm render,
			// as cmd/corpus counts them: later, the caches hold other pages.
			if s.e.name == "cera" {
				if w, err := s.get(); err == nil {
					if r, err := w.call("allocs", strconv.Itoa(i), scale); err == nil {
						pr.allocs, pr.bytes = parseInt(r[0]), parseInt(r[1])
					}
				}
			}
			// A sample of a fast page is several renders, so that it spans
			// minSample: a page of a tenth of a millisecond is otherwise
			// timed mostly by the scheduler.
			pr.reps[s.e.name] = 1
			if *runs > 0 {
				t, _, err := s.render(i, scale, false)
				if err != nil {
					pr.err[s.e.name] = err.Error()
					continue
				}
				pr.reps[s.e.name] = int(min(maxReps, max(1, (int64(minSample)+t-1)/max(t, 1))))
			}
		}
	}

	// The timed runs.
	step := 0
	for k := range *runs {
		for i, pr := range fr.results {
			step++
			for _, s := range rotate(slots, step) {
				if _, failed := pr.err[s.e.name]; failed || len(pr.times[s.e.name]) != k {
					continue
				}
				var sum int64
				n := pr.reps[s.e.name]
				var err error
				for range n {
					var t int64
					if t, _, err = s.render(i, scale, false); err != nil {
						break
					}
					sum += t
				}
				if err != nil {
					pr.err[s.e.name] = err.Error()
					continue
				}
				pr.times[s.e.name] = append(pr.times[s.e.name], sum/int64(n))
			}
		}
	}

	// cera only: rendering again from the display list.
	for _, s := range slots {
		if s.e.name != "cera" {
			continue
		}
		for i, pr := range fr.results {
			if _, failed := pr.err["cera"]; failed {
				continue
			}
			for range *runs {
				w, err := s.get()
				if err != nil {
					break
				}
				if r, err := w.call("again", strconv.Itoa(i), scale); err == nil && parseInt(r[0]) > 0 {
					pr.again = append(pr.again, float64(parseInt(r[1]))/float64(parseInt(r[0])))
				}
			}
		}
	}
	for _, s := range slots {
		if !s.e.wasm {
			if s.w != nil {
				s.rss = max(s.rss, s.w.peakRSS())
			}
			fr.rss[s.e.name] = s.rss
		}
	}

	if *cores > 0 && *runs > 0 && *maxPages == 0 {
		measureAllCores(fr, slots, scale)
	}
	return fr
}

// A document is drawn in pairs on all cores and on one at least
// -multiruns times, and more, up to maxPairs, until the pairs of all
// engines have taken minPairTime.
const (
	maxPairs    = 31
	minPairTime = int64(2e9) // ns
)

// measureAllCores times drawing the whole document on all cores, each
// engine in its own best way: an engine with threads of its own uses them
// (cera: pages concurrently, each drawn in bands on all cores);
// the others run one process per core, as their documentation advises.
// Each run on all cores is paired with the same run on one core (one
// thread, or one process), taking turns, for the engine's gain.
func measureAllCores(fr *fileResult, slots []*slot, scale string) {
	type runner struct {
		name     string
		run, one func() (int64, error)
		stop     func()
	}
	var runners []runner
	for _, s := range slots {
		if s.e.wasm {
			continue
		}
		if fr.failedAny(s.e.name) {
			fr.allErr[s.e.name] = "a page failed"
			continue
		}
		w, err := s.get()
		if err != nil {
			fr.allErr[s.e.name] = err.Error()
			continue
		}
		threads := strconv.Itoa(*cores)
		if _, err := w.call("all", scale, threads); err == nil {
			all := func(threads string) func() (int64, error) {
				return func() (int64, error) {
					w, err := s.get()
					if err != nil {
						return 0, err
					}
					r, err := w.call("all", scale, threads)
					if err != nil {
						return 0, err
					}
					return parseInt(r[0]), nil
				}
			}
			runners = append(runners, runner{s.e.name, all(threads), all("1"), func() {}})
			continue
		} else if !errors.Is(err, errUnsupported) {
			fr.allErr[s.e.name] = err.Error()
			continue
		}
		pool, err := startPool(s.e, s.f, min(*cores, fr.pages))
		if err != nil {
			fr.allErr[s.e.name] = err.Error()
			continue
		}
		single, err := startPool(s.e, s.f, 1)
		if err != nil {
			pool.close()
			fr.allErr[s.e.name] = err.Error()
			continue
		}
		fr.pooled[s.e.name] = true
		pages := fr.pages
		pool.run(pages, scale) // warm every process
		single.run(pages, scale)
		runners = append(runners, runner{s.e.name,
			func() (int64, error) { return pool.run(pages, scale) },
			func() (int64, error) { return single.run(pages, scale) },
			func() { pool.close(); single.close() }})
	}
	// A document drawn in microseconds gets more pairs: a few runs of it
	// measure the scheduler more than the engines (#63).
	var spent int64
	for k := 0; k < *multiRuns || k < maxPairs && spent < minPairTime; k++ {
		for _, r := range rotate(runners, k) {
			if _, failed := fr.allErr[r.name]; failed {
				continue
			}
			// One core and all cores take turns at going first.
			runs := []func() (int64, error){r.run, r.one}
			if k%2 == 1 {
				runs[0], runs[1] = runs[1], runs[0]
			}
			var t [2]int64
			var err error
			for i, run := range runs {
				if t[i], err = run(); err != nil {
					break
				}
			}
			if err != nil {
				fr.allErr[r.name] = err.Error()
				continue
			}
			if k%2 == 1 {
				t[0], t[1] = t[1], t[0]
			}
			spent += t[0] + t[1]
			fr.all[r.name] = append(fr.all[r.name], t[0])
			fr.one[r.name] = append(fr.one[r.name], t[1])
		}
	}
	for _, r := range runners {
		r.stop()
	}
}

func (fr *fileResult) failedAny(engine string) bool {
	if _, ok := fr.openErr[engine]; ok {
		return true
	}
	for _, pr := range fr.results {
		if _, ok := pr.err[engine]; ok {
			return true
		}
	}
	return false
}

// pool is one worker process per core, all with the file open.
type pool struct{ ws []*worker }

func startPool(e *engine, f *file, n int) (*pool, error) {
	p := &pool{}
	errs := make([]error, n)
	p.ws = make([]*worker, n)
	var wg sync.WaitGroup
	for k := range n {
		wg.Go(func() {
			s := &slot{e: e, f: f}
			if _, _, err := s.open(); err != nil {
				errs[k] = err
				if s.w != nil {
					s.w.close()
				}
				return
			}
			p.ws[k] = s.w
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

// run draws every page, each process taking the next page when it is
// done; the time is the wall clock of the whole.
func (p *pool) run(pages int, scale string) (int64, error) {
	next := make(chan int, pages)
	for i := range pages {
		next <- i
	}
	close(next)
	errs := make([]error, len(p.ws))
	var wg sync.WaitGroup
	t0 := clock.Now()
	for k, w := range p.ws {
		wg.Go(func() {
			for i := range next {
				if _, err := w.call("render", strconv.Itoa(i), scale); err != nil {
					errs[k] = err
					return
				}
			}
		})
	}
	wg.Wait()
	ns := clock.Since(t0)
	if err := errors.Join(errs...); err != nil {
		return 0, err
	}
	return int64(ns), nil
}

func (p *pool) close() {
	for _, w := range p.ws {
		if w != nil {
			w.close()
		}
	}
}
