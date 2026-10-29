package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type generatedRows struct {
	pgx.Rows
	n, index, payload int
	closed            bool
	beforeNext        func(int)
	failure           error
}

func (r *generatedRows) Close()     { r.closed = true }
func (r *generatedRows) Err() error { return r.failure }
func (r *generatedRows) Next() bool {
	if r.closed {
		return false
	}
	if r.beforeNext != nil {
		r.beforeNext(r.index)
	}
	if r.index >= r.n {
		r.Close()
		return false
	}
	r.index++
	return true
}
func (r *generatedRows) FieldDescriptions() []pgconn.FieldDescription {
	return []pgconn.FieldDescription{{Name: "id"}, {Name: "payload"}}
}
func (r *generatedRows) Values() ([]any, error) {
	return []any{int64(r.index), strings.Repeat("x", r.payload)}, nil
}
func (r *generatedRows) Scan(dest ...any) error {
	if len(dest) == 1 {
		if scanner, ok := dest[0].(pgx.RowScanner); ok {
			return scanner.ScanRow(r)
		}
	}
	return errors.New("unexpected fixture scan")
}

func TestRowsAreWrittenBeforeReadingTheNextResult(t *testing.T) {
	w := httptest.NewRecorder()
	r := &generatedRows{n: 20, payload: 32}
	r.beforeNext = func(index int) {
		if index > 0 && w.Body.Len() == 0 {
			t.Fatal("the entire result is retained before the response starts")
		}
	}
	(&Server{}).sendRows(w, r, nil)
	var data []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data) != 20 || !r.closed {
		t.Fatal(w.Code, w.Body.String(), r.closed)
	}
}

type memoryResponseWriter struct {
	bytes  uint64
	peak   uint64
	header http.Header
}

func (w *memoryResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *memoryResponseWriter) WriteHeader(int) {}
func (w *memoryResponseWriter) Write(p []byte) (int, error) {
	before := w.bytes
	w.bytes += uint64(len(p))
	if before>>20 != w.bytes>>20 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		w.peak = max(w.peak, m.HeapAlloc)
	}
	return len(p), nil
}
func BenchmarkLargeListResponseMemory(b *testing.B) {
	debug.FreeOSMemory()
	for b.Loop() {
		w := &memoryResponseWriter{}
		(&Server{}).sendRows(w, &generatedRows{n: 4000, payload: 16384}, nil)
		if w.bytes < 64000000 {
			b.Fatal("incomplete list")
		}
		b.ReportMetric(float64(w.peak), "peak-heap-bytes")
	}
}

type failingResponseWriter struct{ memoryResponseWriter }

func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}
func TestRowsStopReadingWhenClientDisconnects(t *testing.T) {
	r := &generatedRows{n: 1000, payload: 32}
	(&Server{}).sendRows(&failingResponseWriter{}, r, nil)
	if !r.closed || r.index > 1 {
		t.Fatal("disconnected client still materializes full result", r.index, r.closed)
	}
}

func TestRowsEmptyTransformAndInterruptedResults(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	r := &generatedRows{}
	s.sendRows(w, r, nil)
	if strings.TrimSpace(w.Body.String()) != "[]" || !r.closed {
		t.Fatal("empty array contract changed", w.Body.String())
	}
	w = httptest.NewRecorder()
	r = &generatedRows{n: 2, payload: 4}
	s.sendRowsWith(w, r, nil, func(row map[string]any) { row["proxy_trusted_cidrs"] = []string{"192.0.2.0/24"} })
	var result []map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result) != 2 || result[1]["proxy_trusted_cidrs"] == nil {
		t.Fatal("per-row resource projection lost", w.Body.String())
	}
	w = httptest.NewRecorder()
	r = &generatedRows{n: 1, payload: 4, failure: errors.New("fixture read interrupted")}
	s.sendRows(w, r, nil)
	if json.Valid(w.Body.Bytes()) || !r.closed {
		t.Fatal("partial database result accepted as a complete list", w.Body.String())
	}
}
