// Package sysfile reads procfs and sysfs attributes that are sampled
// repeatedly. A File is opened once and re-read with pread(2) at offset
// zero on every sample, which the kernel treats as a fresh read of the
// attribute: one syscall per sample instead of the seven os.ReadFile
// costs (open, fcntl, epoll_ctl, fstat, two reads, close). On a Panther
// Lake laptop that is 0.4 µs against 6.4 µs for a cpufreq attribute,
// and the daemon's kernel time is where its overhead goes.
//
// Files are kept open for the life of the collector. A device that goes
// away makes reads fail, which callers treat like any other missing
// metric; nothing here reopens.
package sysfile

import (
	"io/fs"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// File is an attribute held open for repeated sampling.
type File struct {
	path string
	fd   int
	buf  []byte
}

// initialBuf covers every sysfs attribute (a page) and the usual procfs
// files; a larger file grows the buffer on first read and keeps it.
const initialBuf = 4096

// Open opens path read-only. It fails where os.Open would, so callers
// probe availability at init exactly as before.
func Open(path string) (*File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return &File{path: path, fd: fd, buf: make([]byte, initialBuf)}, nil
}

// Path returns the path the file was opened with.
func (f *File) Path() string { return f.path }

// Bytes re-reads the whole file. The returned slice aliases the File's
// buffer and is valid until the next read.
func (f *File) Bytes() ([]byte, error) {
	n := 0
	for {
		if n == len(f.buf) {
			f.buf = append(f.buf, make([]byte, len(f.buf))...)
		}
		m, err := unix.Pread(f.fd, f.buf[n:], int64(n))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, &fs.PathError{Op: "read", Path: f.path, Err: err}
		}
		n += m
		// A short read is the end for sysfs attributes and seq_file
		// procfs entries alike; only a full buffer needs another call.
		if m == 0 || n < len(f.buf) {
			return f.buf[:n], nil
		}
	}
}

// ReadAt reads len(b) bytes at off, for binary regions such as PMT
// telemetry. Short reads are an error.
func (f *File) ReadAt(b []byte, off int64) error {
	n := 0
	for n < len(b) {
		m, err := unix.Pread(f.fd, b[n:], off+int64(n))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return &fs.PathError{Op: "read", Path: f.path, Err: err}
		}
		if m == 0 {
			return &fs.PathError{Op: "read", Path: f.path, Err: unix.EIO}
		}
		n += m
	}
	return nil
}

// String returns the file's contents with surrounding whitespace trimmed.
func (f *File) String() (string, error) {
	b, err := f.Bytes()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Uint parses the file as a decimal unsigned integer.
func (f *File) Uint() (uint64, error) {
	s, err := f.String()
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}

// Int parses the file as a decimal signed integer.
func (f *File) Int() (int64, error) {
	s, err := f.String()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

// Float parses the file as a decimal number.
func (f *File) Float() (float64, error) {
	s, err := f.String()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

// Close releases the descriptor. Safe on nil.
func (f *File) Close() error {
	if f == nil || f.fd < 0 {
		return nil
	}
	err := unix.Close(f.fd)
	f.fd = -1
	return err
}

// OpenUint opens path and checks that it currently holds an unsigned
// integer, the usual "is this attribute usable" probe at init.
func OpenUint(path string) (*File, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Uint(); err != nil {
		f.Close() //nolint:errcheck
		return nil, err
	}
	return f, nil
}

// ReadUint reads one value without keeping the file: for discovery and
// static attributes read once.
func ReadUint(path string) (uint64, error) {
	f, err := Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck
	return f.Uint()
}

// ReadString reads one trimmed value without keeping the file.
func ReadString(path string) (string, error) {
	f, err := Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	return f.String()
}

// ReadInto reads a file once into buf (open, pread, close: three
// syscalls instead of os.ReadFile's seven) and returns the bytes read.
// It is for the per-process files a table walk touches once each; buf
// bounds the read, so size it for the file.
func ReadInto(path string, buf []byte) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(fd) //nolint:errcheck
	n := 0
	for n < len(buf) {
		m, err := unix.Pread(fd, buf[n:], int64(n))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, &fs.PathError{Op: "read", Path: path, Err: err}
		}
		n += m
		if m == 0 || n < len(buf) {
			break
		}
	}
	return buf[:n], nil
}
