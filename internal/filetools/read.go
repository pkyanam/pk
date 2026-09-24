package filetools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultReadLimit     = 200
	maxReadLimit         = 2000
	maxReadOutput        = 16 << 10
	maxReadScanBytes     = 64 << 20
	maxReadFooterReserve = 128
)

var errReadScanLimit = errors.New("Read scan exceeded its 64 MiB limit; use a shell command for distant offsets or larger files")

// readText returns a numbered, bounded page of a UTF-8 text file. Offset is
// one-based. The caller validates/defaults the tool arguments; this function
// repeats the bounds so non-tool callers cannot request unbounded output.
func readText(ctx context.Context, workspace, path string, offset, limit int) (string, error) {
	if offset < 1 {
		return "", errors.New("offset must be a positive, 1-based line number")
	}
	if limit < 1 || limit > maxReadLimit {
		return "", fmt.Errorf("limit must be between 1 and %d lines", maxReadLimit)
	}
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') {
		return "", errors.New("path must be non-empty and cannot contain NUL")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve file path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("Read supports regular files only")
	}
	file, err := openReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return "", errors.New("file changed while opening; retry Read")
	}

	reader := bufio.NewReaderSize(&scanLimitReader{reader: file, remaining: maxReadScanBytes}, 4096)
	lineNo := 1
	for lineNo < offset {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_, found, err := readLine(ctx, reader, false)
		if err != nil {
			return "", fmt.Errorf("scan toward line %d: %w", lineNo, err)
		}
		if !found {
			return fmt.Sprintf("[End of file before line %d.]\n", offset), nil
		}
		lineNo++
	}

	var out bytes.Buffer
	lines := 0
	for lines < limit {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		line, found, err := readLine(ctx, reader, true)
		if err != nil {
			return "", fmt.Errorf("read line %d: %w", lineNo, err)
		}
		if !found {
			return appendReadFooter(&out, "[End of file.]\n")
		}
		if !utf8.Valid(line) {
			return "", fmt.Errorf("line %d is not valid UTF-8 text; Read supports UTF-8 text files only", lineNo)
		}
		content := line
		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
			if len(content) > 0 && content[len(content)-1] == '\r' {
				content = content[:len(content)-1]
			}
		}
		for _, r := range string(content) {
			if r < 0x20 && r != '\t' || r >= 0x7f && r <= 0x9f {
				return "", fmt.Errorf("line %d contains terminal control characters; refusing unsafe text output", lineNo)
			}
		}
		var headerBuffer [32]byte
		header := strconv.AppendInt(headerBuffer[:0], int64(lineNo), 10)
		header = append(header, ' ', '|', ' ')
		formattedLen := len(header) + len(content) + 1
		if out.Len()+formattedLen+maxReadFooterReserve > maxReadOutput {
			if out.Len() == 0 {
				return "", fmt.Errorf("line %d is too long for Read's %d-byte output cap; use a byte-range or shell command for this line", lineNo, maxReadOutput)
			}
			return appendReadFooter(&out, fmt.Sprintf("[Output cap reached. Continue with offset %d.]\n", lineNo))
		}
		out.Write(header)
		out.Write(content)
		out.WriteByte('\n')
		lines++
		lineNo++
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, err = reader.Peek(1)
	if errors.Is(err, io.EOF) {
		return appendReadFooter(&out, "[End of file.]\n")
	}
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
		if errors.Is(err, errReadScanLimit) {
			return appendReadFooter(&out, "[Read scan limit reached; use Bash with sed/head/tail for this distant range.]\n")
		}
		return "", fmt.Errorf("check for more lines: %w", err)
	}
	return appendReadFooter(&out, fmt.Sprintf("[Continue with offset %d.]\n", lineNo))
}

// readLine reads a complete line while keeping memory bounded. When collect is
// false, it drains a skipped line without retaining it. Any NUL marks the
// input as binary. Collected lines are capped before appending another chunk.
func readLine(ctx context.Context, reader *bufio.Reader, collect bool) ([]byte, bool, error) {
	var line []byte
	var utf8Tail []byte
	sawBytes := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		fragment, err := reader.ReadSlice('\n')
		if bytes.IndexByte(fragment, 0) >= 0 {
			return nil, false, errors.New("file appears to be binary (contains NUL bytes); Read supports text files only")
		}
		if bytes.IndexByte(fragment, 0x1b) >= 0 {
			return nil, false, errors.New("file contains terminal escape sequences; refusing unsafe text output")
		}
		if len(fragment) > 0 {
			sawBytes = true
		}
		final := err == nil || errors.Is(err, io.EOF) && (sawBytes || len(line) > 0)
		if utf8Err := validateUTF8Chunk(&utf8Tail, fragment, final); utf8Err != nil {
			return nil, false, errors.New("file is not valid UTF-8 text")
		}
		if collect {
			if len(line)+len(fragment) > maxReadOutput {
				return nil, false, errors.New("line exceeds Read's 16 KiB output cap; use a byte-range or shell command for this line")
			}
			line = append(line, fragment...)
		}
		if err == nil {
			return line, true, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if sawBytes {
				return line, true, nil
			}
			return nil, false, nil
		}
		if errors.Is(err, errReadScanLimit) {
			return nil, false, errReadScanLimit
		}
		return nil, false, fmt.Errorf("read line: %w", err)
	}
}

func validateUTF8Chunk(tail *[]byte, chunk []byte, final bool) error {
	data := chunk
	if len(*tail) != 0 {
		joined := make([]byte, 0, len(*tail)+len(chunk))
		joined = append(joined, (*tail)...)
		data = append(joined, chunk...)
	}
	index := 0
	for index < len(data) {
		if !utf8.FullRune(data[index:]) {
			break
		}
		r, size := utf8.DecodeRune(data[index:])
		if r == utf8.RuneError && size == 1 {
			return errors.New("invalid UTF-8")
		}
		index += size
	}
	if index == len(data) {
		*tail = (*tail)[:0]
	} else {
		*tail = append((*tail)[:0], data[index:]...)
	}
	if final && len(*tail) != 0 {
		return errors.New("truncated UTF-8 sequence")
	}
	return nil
}

type scanLimitReader struct {
	reader    io.Reader
	remaining int64
}

func (r *scanLimitReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errReadScanLimit
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func appendReadFooter(out *bytes.Buffer, footer string) (string, error) {
	if out.Len()+len(footer) > maxReadOutput {
		return "", errors.New("Read output exceeded its hard size cap")
	}
	out.WriteString(footer)
	return out.String(), nil
}
