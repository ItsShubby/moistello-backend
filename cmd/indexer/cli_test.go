package main

import (
	"errors"
	"testing"
)

type fakeCloser struct {
	err    error
	closed int
}

func (f *fakeCloser) Close() error {
	f.closed++
	return f.err
}

func TestCloseInto_SuccessLeavesErrUnchanged(t *testing.T) {
	c := &fakeCloser{}
	var err error
	closeInto(c, "database", &err)
	if c.closed != 1 {
		t.Fatalf("Close called %d times, want 1", c.closed)
	}
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCloseInto_SuccessKeepsExistingErr(t *testing.T) {
	orig := errors.New("query failed")
	err := orig
	closeInto(&fakeCloser{}, "database", &err)
	if err != orig {
		t.Fatalf("err = %v, want original error unchanged", err)
	}
}

func TestCloseInto_CloseFailureIsReported(t *testing.T) {
	closeErr := errors.New("connection reset")
	var err error
	closeInto(&fakeCloser{err: closeErr}, "database", &err)
	if !errors.Is(err, closeErr) {
		t.Fatalf("err = %v, want it to wrap the close error", err)
	}
	if got, want := err.Error(), "closing database: connection reset"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}
}

func TestCloseInto_CloseFailureJoinsExistingErr(t *testing.T) {
	orig := errors.New("query failed")
	closeErr := errors.New("connection reset")
	err := orig
	closeInto(&fakeCloser{err: closeErr}, "database", &err)
	if !errors.Is(err, orig) {
		t.Fatalf("err = %v, want it to keep the original error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("err = %v, want it to include the close error", err)
	}
}

func TestLogClose_ClosesOnSuccessAndFailure(t *testing.T) {
	ok := &fakeCloser{}
	logClose(ok, "redis")
	if ok.closed != 1 {
		t.Fatalf("Close called %d times, want 1", ok.closed)
	}

	failing := &fakeCloser{err: errors.New("boom")}
	logClose(failing, "redis")
	if failing.closed != 1 {
		t.Fatalf("Close called %d times, want 1", failing.closed)
	}
}
