package execx

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Compile-time assertions that the implementations satisfy the interface.
var (
	_ Runner = (*ExecRunner)(nil)
	_ Runner = (*FakeRunner)(nil)
)

// TestMain turns the test binary into a controllable subprocess when the
// EXECX_HELPER environment variable is set. This lets the tests exercise the
// real os/exec path without invoking a shell.
func TestMain(m *testing.M) {
	if os.Getenv("EXECX_HELPER") == "1" {
		helperProcess()
		return // helperProcess always exits
	}
	os.Exit(m.Run())
}

func helperProcess() {
	switch os.Getenv("EXECX_HELPER_MODE") {
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "ok":
		fmt.Fprint(os.Stdout, "hello stdout")
		fmt.Fprint(os.Stderr, "hello stderr")
		os.Exit(0)
	default:
		fmt.Fprint(os.Stdout, "hello stdout")
		fmt.Fprint(os.Stderr, "hello stderr")
		os.Exit(3)
	}
}

// runSelf invokes this test binary as a helper subprocess.
func runSelf(t *testing.T, ctx context.Context, mode string) (string, string, error) {
	t.Helper()
	t.Setenv("EXECX_HELPER", "1")
	t.Setenv("EXECX_HELPER_MODE", mode)
	return New().Run(ctx, os.Args[0], "-test.run=^$")
}

func TestExitCodeNil(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Errorf("ExitCode(nil) = %d, want 0", got)
	}
}

func TestExitCodeRealProcess(t *testing.T) {
	_, _, err := runSelf(t, context.Background(), "exit")
	if err == nil {
		t.Fatal("expected a non-zero exit error, got nil")
	}
	if got := ExitCode(err); got != 3 {
		t.Errorf("ExitCode(err) = %d, want 3", got)
	}
}

func TestExitCodeNotAnExitError(t *testing.T) {
	_, _, err := New().Run(context.Background(), "execx-definitely-not-a-real-binary")
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
	if got := ExitCode(err); got != -1 {
		t.Errorf("ExitCode(err) = %d, want -1", got)
	}
}

func TestExecRunnerCapturesOutput(t *testing.T) {
	stdout, stderr, err := runSelf(t, context.Background(), "ok")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(stdout, "hello stdout") {
		t.Errorf("stdout = %q, want to contain %q", stdout, "hello stdout")
	}
	if !strings.Contains(stderr, "hello stderr") {
		t.Errorf("stderr = %q, want to contain %q", stderr, "hello stderr")
	}
}

func TestExecRunnerHonoursContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := runSelf(t, ctx, "sleep")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %s; context deadline was not honoured", elapsed)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}
}

func TestFakeRunnerRecordsInvocations(t *testing.T) {
	fake := NewFakeRunner().
		On("usbip", []string{"list", "-p"}, FakeResponse{Stdout: "list output"}).
		OnName("usbipd", FakeResponse{Stdout: "daemon ok"}).
		SetFallback(FakeResponse{Stdout: "fallback"})

	out, _, err := fake.Run(context.Background(), "usbip", "list", "-p")
	if err != nil || out != "list output" {
		t.Fatalf("exact match = (%q, %v), want (%q, nil)", out, err, "list output")
	}

	out, _, _ = fake.Run(context.Background(), "usbipd", "--version")
	if out != "daemon ok" {
		t.Errorf("by-name match = %q, want %q", out, "daemon ok")
	}

	out, _, _ = fake.Run(context.Background(), "other")
	if out != "fallback" {
		t.Errorf("fallback = %q, want %q", out, "fallback")
	}

	if got := fake.CallCount(); got != 3 {
		t.Fatalf("CallCount() = %d, want 3", got)
	}
	calls := fake.Calls()
	if len(calls) != 3 {
		t.Fatalf("len(Calls()) = %d, want 3", len(calls))
	}
	if calls[0].Name != "usbip" || strings.Join(calls[0].Args, " ") != "list -p" {
		t.Errorf("first call = %+v, want usbip [list -p]", calls[0])
	}
}

func TestFakeRunnerRecordsError(t *testing.T) {
	wantErr := fmt.Errorf("boom")
	fake := NewFakeRunner().OnName("usbip", FakeResponse{Stdout: "out", Stderr: "err", Err: wantErr})
	stdout, stderr, err := fake.Run(context.Background(), "usbip", "attach")
	if stdout != "out" || stderr != "err" || err != wantErr {
		t.Errorf("got (%q, %q, %v), want (out, err, %v)", stdout, stderr, err, wantErr)
	}
}
