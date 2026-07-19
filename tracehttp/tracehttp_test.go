package tracehttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/latnikovs/slogx"
)

func TestParseCloudTraceHeader(t *testing.T) {
	cases := []struct {
		name        string
		header      string
		wantOK      bool
		wantTraceID string
		wantSpanID  string
		wantSampled bool
	}{
		{
			name:        "well-formed with span and sampling on",
			header:      "105445aa7843bc8bf206b12000100000/1;o=1",
			wantOK:      true,
			wantTraceID: "105445aa7843bc8bf206b12000100000",
			wantSpanID:  "0000000000000001", // decimal 1 -> 16-char hex
			wantSampled: true,
		},
		{
			name:        "sampling off",
			header:      "105445aa7843bc8bf206b12000100000/255;o=0",
			wantOK:      true,
			wantTraceID: "105445aa7843bc8bf206b12000100000",
			wantSpanID:  "00000000000000ff", // decimal 255 -> hex ff
			wantSampled: false,
		},
		{
			name:        "span present, no options",
			header:      "105445aa7843bc8bf206b12000100000/4096",
			wantOK:      true,
			wantTraceID: "105445aa7843bc8bf206b12000100000",
			wantSpanID:  "0000000000001000", // decimal 4096 -> hex 1000
			wantSampled: false,
		},
		{
			name:        "span-less header",
			header:      "105445aa7843bc8bf206b12000100000",
			wantOK:      true,
			wantTraceID: "105445aa7843bc8bf206b12000100000",
			wantSpanID:  "",
			wantSampled: false,
		},
		{
			name:        "trace with options but no span",
			header:      "105445aa7843bc8bf206b12000100000/;o=1",
			wantOK:      true,
			wantTraceID: "105445aa7843bc8bf206b12000100000",
			wantSpanID:  "",
			wantSampled: true,
		},
		{
			name:        "zero span is dropped",
			header:      "105445aa7843bc8bf206b12000100000/0;o=1",
			wantOK:      true,
			wantTraceID: "105445aa7843bc8bf206b12000100000",
			wantSpanID:  "",
			wantSampled: true,
		},
		{
			name:        "non-numeric span is dropped, trace still parsed",
			header:      "105445aa7843bc8bf206b12000100000/abc",
			wantOK:      true,
			wantTraceID: "105445aa7843bc8bf206b12000100000",
			wantSpanID:  "",
		},
		{name: "empty header", header: "", wantOK: false},
		{name: "whitespace header", header: "   ", wantOK: false},
		{name: "malformed leading slash", header: "/1;o=1", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := ParseCloudTraceHeader(tc.header)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (info=%+v)", ok, tc.wantOK, info)
			}
			if !tc.wantOK {
				return
			}
			if info.TraceID != tc.wantTraceID {
				t.Errorf("TraceID = %q, want %q", info.TraceID, tc.wantTraceID)
			}
			if info.SpanID != tc.wantSpanID {
				t.Errorf("SpanID = %q, want %q", info.SpanID, tc.wantSpanID)
			}
			if info.Sampled != tc.wantSampled {
				t.Errorf("Sampled = %v, want %v", info.Sampled, tc.wantSampled)
			}
		})
	}
}

func TestMiddlewareStoresTraceInContext(t *testing.T) {
	var got slogx.TraceInfo
	var present bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, present = slogx.TraceFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Cloud-Trace-Context", "105445aa7843bc8bf206b12000100000/1;o=1")
	Middleware()(next).ServeHTTP(httptest.NewRecorder(), req)

	if !present {
		t.Fatal("no trace stored in context")
	}
	if got.TraceID != "105445aa7843bc8bf206b12000100000" {
		t.Errorf("TraceID = %q", got.TraceID)
	}
	if got.SpanID != "0000000000000001" {
		t.Errorf("SpanID = %q, want hex-converted span", got.SpanID)
	}
	if !got.Sampled {
		t.Error("Sampled = false, want true")
	}
}

func TestMiddlewarePassesThroughWithoutHeader(t *testing.T) {
	called := false
	var present bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		_, present = slogx.TraceFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil) // no trace header
	Middleware()(next).ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("next handler was not called")
	}
	if present {
		t.Error("trace present in context despite missing header")
	}
}

func TestMiddlewarePassesThroughMalformedHeader(t *testing.T) {
	var present bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, present = slogx.TraceFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Cloud-Trace-Context", "/malformed")
	Middleware()(next).ServeHTTP(httptest.NewRecorder(), req)

	if present {
		t.Error("malformed header stored a trace in context")
	}
}

// TestMiddlewarePreservesContext ensures the middleware augments, rather than
// replaces, the incoming request context.
func TestMiddlewarePreservesContext(t *testing.T) {
	type ctxKey struct{}
	var carried any
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		carried = r.Context().Value(ctxKey{})
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, "sentinel"))
	req.Header.Set("X-Cloud-Trace-Context", "105445aa7843bc8bf206b12000100000/1;o=1")
	Middleware()(next).ServeHTTP(httptest.NewRecorder(), req)

	if carried != "sentinel" {
		t.Fatalf("upstream context value lost: got %v", carried)
	}
}
