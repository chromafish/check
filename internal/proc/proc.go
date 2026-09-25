// Package proc runs a command and hands back its output as it is written.
//
// Every command the repository is read through passes here, so this is
// where each one is written to the behaviour log, with its arguments and how
// it ended.
package proc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// waitDelay bounds how long Close waits on a command that will not go, or on
// pipes something else is still holding open.
const waitDelay = 5 * time.Second

// Run executes a command in dir and returns what it wrote to standard output.
// A command that fails is reported by what it printed on standard error, which
// is the message a person would have seen at a terminal.
func Run(ctx context.Context, dir, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	ran(cmd, err)
	if err != nil {
		return nil, said(err, &stderr)
	}
	return stdout.Bytes(), nil
}

// RunTee is Run for a command a person is watching: env is added to the
// environment, and standard error is copied to progress as it is written,
// as well as kept for the error. It is how a clone shows what git is doing.
func RunTee(ctx context.Context, dir, bin string, env []string, progress io.Writer, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(&stderr, progress)
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	ran(cmd, err)
	if err != nil {
		return nil, said(err, &stderr)
	}
	return stdout.Bytes(), nil
}

// said prefers what the command printed over the exit status it died with.
func said(err error, stderr *bytes.Buffer) error {
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return errors.New(msg)
	}
	return err
}

// ran writes a finished command to the log. The line is assembled only when
// it will be written, so a command run with the log off pays nothing for it.
// A failure is a fact about the command and not yet about the program, so it
// is written at the same level as a success, with the exit status to say so;
// what it meant is logged where the error is handled.
func ran(cmd *exec.Cmd, err error) {
	if !slog.Default().Enabled(context.Background(), slog.LevelInfo) {
		return
	}
	attrs := []any{"cmd", filepath.Base(cmd.Path), "args", strings.Join(cmd.Args[1:], " "), "dir", cmd.Dir}
	switch state := cmd.ProcessState; {
	case state == nil:
		attrs = append(attrs, "err", err.Error())
	case state.Exited():
		attrs = append(attrs, "exit", state.ExitCode())
	default:
		attrs = append(attrs, "signal", signalOf(state))
	}
	slog.Info("command ran", attrs...)
}

func signalOf(state *os.ProcessState) string {
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ws.Signal().String()
	}
	return state.String()
}

// Stream starts a command in dir and returns its standard output, which the
// caller reads while the command is still producing it.
//
// The reader must be closed: closing is what reaps the command, and so what
// reports a failure the command signals only by exiting.
func Stream(ctx context.Context, dir, bin string, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Killing the command does not free a read blocked on a pipe one of its
	// own children still holds open, and Wait would then block behind that
	// read. Closing the pipe is what ends it; WaitDelay bounds the rest.
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		out.Close()
		return err
	}
	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		ran(cmd, err)
		return nil, err
	}
	return &output{out: out, cmd: cmd, stderr: &stderr}, nil
}

type output struct {
	out    io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (o *output) Read(b []byte) (int, error) { return o.out.Read(b) }

// Close reaps the command. Every way it can fail is reported, including the
// ones that leave usable output behind: a diff cut short is not a diff, and
// has to be reported rather than passed off as a change with a piece missing.
func (o *output) Close() error {
	o.out.Close()
	err := o.cmd.Wait()
	ran(o.cmd, err)
	if err == nil {
		return nil
	}
	return said(err, o.stderr)
}
