package engine

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCaptureDelimitedRecords_DelimitersAndEOFTail(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "line feeds", input: "one\ntwo\n", want: []string{"one", "two"}},
		{name: "carriage returns", input: "one\rtwo\r", want: []string{"one", "two"}},
		{name: "CRLF", input: "one\r\ntwo\r\n", want: []string{"one", "two"}},
		{name: "empty delimiters", input: "\r\n\n\rone\r\r\n\n", want: []string{"one"}},
		{name: "EOF tail", input: "one\ntail", want: []string{"one", "tail"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			err := captureDelimitedRecords(strings.NewReader(test.input), func(record string) {
				got = append(got, record)
			})
			if err != nil {
				t.Fatalf("captureDelimitedRecords() error = %v", err)
			}
			if strings.Join(got, "|") != strings.Join(test.want, "|") {
				t.Errorf("records = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCaptureDelimitedRecordsWithOrigin_StandaloneCarriageReturnMarksNextRecord(t *testing.T) {
	type captured struct {
		text                string
		afterCarriageReturn bool
	}
	var records []captured

	err := captureDelimitedRecordsWithOrigin(
		strings.NewReader("file\n\rprogress\r\nnext"),
		func(record string, afterCarriageReturn bool) {
			records = append(records, captured{
				text:                record,
				afterCarriageReturn: afterCarriageReturn,
			})
		},
	)

	if err != nil {
		t.Fatalf("captureDelimitedRecordsWithOrigin() error = %v", err)
	}
	want := []captured{
		{text: "file"},
		{text: "progress", afterCarriageReturn: true},
		{text: "next"},
	}
	if len(records) != len(want) {
		t.Fatalf("records = %+v, want %+v", records, want)
	}
	for index := range want {
		if records[index] != want[index] {
			t.Errorf("record %d = %+v, want %+v", index, records[index], want[index])
		}
	}
}

func TestCaptureDelimitedRecords_OversizedRecordTruncatesAndContinuesDraining(t *testing.T) {
	oversized := strings.Repeat("x", outputRecordLimitBytes+17)
	input := oversized + "\nnormal\n"
	var records []string

	err := captureDelimitedRecords(strings.NewReader(input), func(record string) {
		records = append(records, record)
	})

	if err != nil {
		t.Fatalf("captureDelimitedRecords() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	wantFirst := strings.Repeat("x", outputRecordLimitBytes) + outputTruncationSuffix
	if records[0] != wantFirst {
		t.Errorf("oversized record length = %d, want %d with one truncation suffix", len(records[0]), len(wantFirst))
	}
	if records[1] != "normal" {
		t.Errorf("record after oversized token = %q, want %q", records[1], "normal")
	}
}

var errCaptureRead = errors.New("injected read failure")

type recordThenErrorReader struct {
	recorded bool
}

func (r *recordThenErrorReader) Read(p []byte) (int, error) {
	if r.recorded {
		return 0, errCaptureRead
	}
	r.recorded = true
	return copy(p, "tail without delimiter"), errCaptureRead
}

func TestCaptureDelimitedRecords_NonEOFReadErrorFlushesTail(t *testing.T) {
	var records []string

	err := captureDelimitedRecords(&recordThenErrorReader{}, func(record string) {
		records = append(records, record)
	})

	if !errors.Is(err, errCaptureRead) {
		t.Fatalf("captureDelimitedRecords() error = %v, want injected read failure", err)
	}
	if len(records) != 1 || records[0] != "tail without delimiter" {
		t.Errorf("records = %q, want flushed EOF-style tail", records)
	}
}

func TestTailBuffer_RetainsOnlyConfiguredSuffix(t *testing.T) {
	tail := newTailBuffer(5)
	for _, chunk := range [][]byte{[]byte("ab"), []byte("cdef"), []byte("XYZ")} {
		if written, err := tail.Write(chunk); err != nil || written != len(chunk) {
			t.Fatalf("Write(%q) = (%d, %v)", chunk, written, err)
		}
	}
	if got, want := string(tail.Bytes()), "efXYZ"; got != want {
		t.Errorf("Bytes() = %q, want %q", got, want)
	}

	empty := newTailBuffer(0)
	if written, err := empty.Write([]byte("discard")); err != nil || written != len("discard") {
		t.Fatalf("zero-capacity Write() = (%d, %v)", written, err)
	}
	if got := empty.Bytes(); len(got) != 0 {
		t.Errorf("zero-capacity Bytes() = %q, want empty", got)
	}
}

func TestTailBuffer_MultipleWrapsPreserveSuffixOrder(t *testing.T) {
	tail := newTailBuffer(5)
	for _, chunk := range [][]byte{
		[]byte("abcde"),
		[]byte("f"),
		[]byte("gh"),
		[]byte("ijk"),
		[]byte("lmno"),
	} {
		if written, err := tail.Write(chunk); err != nil || written != len(chunk) {
			t.Fatalf("Write(%q) = (%d, %v)", chunk, written, err)
		}
	}
	if got, want := string(tail.Bytes()), "klmno"; got != want {
		t.Errorf("Bytes() = %q, want %q", got, want)
	}
}

func TestTailBuffer_LargeWriteReplacesWrappedContent(t *testing.T) {
	tail := newTailBuffer(5)
	_, _ = tail.Write([]byte("abcdef"))
	_, _ = tail.Write([]byte("gh"))

	if written, err := tail.Write([]byte("0123456789")); err != nil || written != 10 {
		t.Fatalf("large Write() = (%d, %v), want (10, nil)", written, err)
	}
	if got, want := string(tail.Bytes()), "56789"; got != want {
		t.Errorf("Bytes() = %q, want %q", got, want)
	}
}

func TestTailBuffer_SmallWritePastCapacityAdvancesCircularStart(t *testing.T) {
	tail := newTailBuffer(4)
	_, _ = tail.Write([]byte("abcd"))
	_, _ = tail.Write([]byte("e"))

	if tail.start != 1 {
		t.Errorf("circular start = %d, want 1 after overwriting one byte", tail.start)
	}
}

type oneShotDataAndEOFReader struct {
	data      string
	readCalls int
}

func (r *oneShotDataAndEOFReader) Read(p []byte) (int, error) {
	r.readCalls++
	if r.readCalls > 1 {
		return 0, io.EOF
	}
	return copy(p, r.data), io.EOF
}

func TestCaptureDelimitedRecords_ReaderMayReturnDataWithEOF(t *testing.T) {
	var records []string
	reader := &oneShotDataAndEOFReader{data: "record"}
	if err := captureDelimitedRecords(reader, func(record string) {
		records = append(records, record)
	}); err != nil {
		t.Fatalf("captureDelimitedRecords() error = %v", err)
	}
	if len(records) != 1 || records[0] != "record" {
		t.Errorf("records = %q, want EOF tail", records)
	}
	if reader.readCalls != 1 {
		t.Errorf("Read calls = %d, want 1 data-plus-EOF call", reader.readCalls)
	}
}
