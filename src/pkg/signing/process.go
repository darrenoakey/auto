package signing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	processTimeout   = 90 * time.Second
	processWaitDelay = 5 * time.Second
	outputLimit      = 64 * 1024
)

type boundedWriter struct {
	buffer bytes.Buffer
	limit  int
	mutex  sync.Mutex
}

func runProcess(parent context.Context, name string, arguments []string, secrets ...[]byte) (string, error) {
	operationContext, cancel := context.WithTimeout(parent, processTimeout)
	defer cancel()
	readers, writers, err := secretPipes(secrets)
	if err != nil {
		return "", fmt.Errorf("create anonymous secret descriptors")
	}
	defer closeFiles(readers)
	defer closeFiles(writers)
	command, output := newCommand(operationContext, name, arguments, readers)
	if err = command.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", name, err)
	}
	closeFiles(readers)
	written := writeSecrets(writers, secrets)
	err = command.Wait()
	if err != nil {
		closeFiles(writers)
	}
	if writeErr := <-written; writeErr != nil {
		err = errors.Join(err, writeErr)
	}
	if err != nil {
		return "", processError(name, err, output.String(), len(secrets) != 0)
	}
	if len(secrets) != 0 {
		return "", nil
	}
	return output.String(), nil
}

func secretPipes(secrets [][]byte) ([]*os.File, []*os.File, error) {
	readers := make([]*os.File, 0, len(secrets))
	writers := make([]*os.File, 0, len(secrets))
	for range secrets {
		reader, writer, err := os.Pipe()
		if err != nil {
			closeFiles(readers)
			closeFiles(writers)
			return nil, nil, err
		}
		readers = append(readers, reader)
		writers = append(writers, writer)
	}
	return readers, writers, nil
}

func newCommand(context context.Context, name string, arguments []string, readers []*os.File) (*exec.Cmd, *boundedWriter) {
	command := exec.CommandContext(context, name, arguments...)
	command.Env = []string{}
	command.ExtraFiles = readers
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output := &boundedWriter{limit: outputLimit}
	command.Stdout = output
	command.Stderr = output
	command.Cancel = func() error { return killProcessGroup(command) }
	command.WaitDelay = processWaitDelay
	return command, output
}

func writeSecrets(writers []*os.File, secrets [][]byte) <-chan error {
	result := make(chan error, 1)
	go func() {
		var firstError error
		for index, writer := range writers {
			if _, err := writer.Write(secrets[index]); err != nil && firstError == nil {
				firstError = fmt.Errorf("write anonymous secret descriptor")
			}
			if err := writer.Close(); err != nil && firstError == nil {
				firstError = fmt.Errorf("close anonymous secret descriptor")
			}
		}
		result <- firstError
	}()
	return result
}

func killProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processError(name string, err error, output string, secretBearing bool) error {
	if secretBearing || strings.TrimSpace(output) == "" {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(output))
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	remaining := writer.limit - writer.buffer.Len()
	if remaining > 0 {
		_, _ = writer.buffer.Write(value[:min(remaining, len(value))])
	}
	return len(value), nil
}

func (writer *boundedWriter) String() string {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.buffer.String()
}

var _ io.Writer = (*boundedWriter)(nil)
