package execx

import (
	"context"
	"strings"
	"sync"
)

// Invocation records one call made through a FakeRunner.
type Invocation struct {
	Name string
	Args []string
}

// FakeResponse is the programmable result of a fake invocation.
type FakeResponse struct {
	Stdout string
	Stderr string
	Err    error
}

// FakeRunner is a test double for Runner. It records every invocation and
// returns programmable responses, keyed either by the exact name+args pair or
// by the binary name, with a configurable fallback.
//
// It is safe for concurrent use.
type FakeRunner struct {
	mu        sync.Mutex
	calls     []Invocation
	byCommand map[string]FakeResponse
	byName    map[string]FakeResponse
	fallback  FakeResponse
}

// NewFakeRunner returns an empty FakeRunner.
func NewFakeRunner() *FakeRunner {
	return &FakeRunner{
		byCommand: make(map[string]FakeResponse),
		byName:    make(map[string]FakeResponse),
	}
}

func commandKey(name string, args []string) string {
	return name + "\x00" + strings.Join(args, "\x00")
}

// On programs the response for the exact name and args. It returns the runner
// so calls can be chained.
func (f *FakeRunner) On(name string, args []string, resp FakeResponse) *FakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byCommand[commandKey(name, args)] = resp
	return f
}

// OnName programs the response for every call to name that is not matched by an
// exact name+args rule.
func (f *FakeRunner) OnName(name string, resp FakeResponse) *FakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byName[name] = resp
	return f
}

// SetFallback programs the response returned when nothing else matches.
func (f *FakeRunner) SetFallback(resp FakeResponse) *FakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback = resp
	return f
}

// Run records the invocation and returns the best matching programmed response.
func (f *FakeRunner) Run(_ context.Context, name string, args ...string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	recorded := append([]string(nil), args...)
	f.calls = append(f.calls, Invocation{Name: name, Args: recorded})

	if resp, ok := f.byCommand[commandKey(name, args)]; ok {
		return resp.Stdout, resp.Stderr, resp.Err
	}
	if resp, ok := f.byName[name]; ok {
		return resp.Stdout, resp.Stderr, resp.Err
	}
	return f.fallback.Stdout, f.fallback.Stderr, f.fallback.Err
}

// Calls returns a copy of every recorded invocation.
func (f *FakeRunner) Calls() []Invocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Invocation, len(f.calls))
	for i, c := range f.calls {
		out[i] = Invocation{Name: c.Name, Args: append([]string(nil), c.Args...)}
	}
	return out
}

// CallCount returns how many invocations have been recorded.
func (f *FakeRunner) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}
