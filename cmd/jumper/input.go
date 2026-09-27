package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
)

// terminalInput reads stdin in a single goroutine, so the menu (line input)
// and an ssh session (raw bytes) can take turns without losing keystrokes
// to a reader that is still blocked on stdin.
type terminalInput struct {
	chunks  chan []byte // data read from stdin; closed at EOF
	mu      sync.Mutex
	pending []byte // bytes received but not yet consumed
}

// newTerminalInput starts the stdin reader goroutine.
func newTerminalInput() *terminalInput {
	in := &terminalInput{chunks: make(chan []byte)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				in.chunks <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(in.chunks)
				return
			}
		}
	}()
	return in
}

// next returns pending bytes or waits for the next chunk. ok is false at
// EOF or once done is closed (a nil done never closes).
func (in *terminalInput) next(done <-chan struct{}) (b []byte, ok bool) {
	in.mu.Lock()
	if len(in.pending) > 0 {
		b, in.pending = in.pending, nil
		in.mu.Unlock()
		return b, true
	}
	in.mu.Unlock()
	select {
	case b, ok = <-in.chunks:
		return b, ok
	case <-done:
		return nil, false
	}
}

// unread keeps b for the next reader.
func (in *terminalInput) unread(b []byte) {
	if len(b) == 0 {
		return
	}
	in.mu.Lock()
	in.pending = append(b, in.pending...)
	in.mu.Unlock()
}

// ReadLine returns the next input line without its line ending.
func (in *terminalInput) ReadLine() (string, error) {
	var line []byte
	for {
		b, ok := in.next(nil)
		if !ok {
			if len(line) > 0 {
				return string(line), nil
			}
			return "", io.EOF
		}
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line = append(line, b[:i]...)
			in.unread(b[i+1:])
			return strings.TrimRight(string(line), "\r"), nil
		}
		line = append(line, b...)
	}
}

// sessionReader passes stdin to an ssh session until done is closed.
type sessionReader struct {
	in   *terminalInput
	done <-chan struct{}
}

// Read implements io.Reader; it returns io.EOF once the session is over.
func (r sessionReader) Read(p []byte) (int, error) {
	b, ok := r.in.next(r.done)
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, b)
	r.in.unread(b[n:])
	return n, nil
}
