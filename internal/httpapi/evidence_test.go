package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/evidence"
	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
)

// fakeEvidence 是归档端口的无库实现：测试只验证 HTTP 面的解码/分诊，
// 脱敏与哈希仍走 `internal/evidence` 的真实现。
type fakeEvidence struct {
	records map[string]evidence.Record
}

func newFakeEvidence() *fakeEvidence {
	return &fakeEvidence{records: map[string]evidence.Record{}}
}

func (f *fakeEvidence) Put(_ context.Context, rec evidence.Record) (bool, error) {
	if _, ok := f.records[rec.ContentHash]; ok {
		return false, nil
	}
	f.records[rec.ContentHash] = rec
	return true, nil
}

func (f *fakeEvidence) Get(_ context.Context, contentHash string) (*evidence.Record, error) {
	rec, ok := f.records[contentHash]
	if !ok {
		return nil, serving.ErrEvidenceNotFound
	}
	return &rec, nil
}

func (f *fakeEvidence) List(_ context.Context, filter store.EvidenceFilter) ([]evidence.Record, error) {
	out := make([]evidence.Record, 0, len(f.records))
	for _, rec := range f.records {
		if filter.Source != "" && rec.Source != filter.Source {
			continue
		}
		if filter.Dataset != "" && rec.Dataset != filter.Dataset {
			continue
		}
		out = append(out, rec)
	}
	if len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func newEvidenceServer(archive EvidenceArchive) *Server {
	return New(nil, WithAuthorizer(allowAllAuthorizer{}), WithEvidence(archive))
}

func postRaw(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/ingest/raw", strings.NewReader(body)))
	return rec
}

// 破坏验证 ③：把 ErrEvidenceNotFound 映射成 500 后本用例必须红。
func TestGetEvidenceMissingIs404(t *testing.T) {
	srv := newEvidenceServer(newFakeEvidence())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evidence/"+strings.Repeat("0", 64), nil))

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "evidence_not_found", body.Error.Code)
}

func TestGetEvidenceRejectsMalformedHash(t *testing.T) {
	srv := newEvidenceServer(newFakeEvidence())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evidence/not-a-hash", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "invalid_parameter", body.Error.Code)
}

func TestIngestRawRedactsBeforeHashAndGetReturnsMasked(t *testing.T) {
	srv := newEvidenceServer(newFakeEvidence())
	rec := postRaw(t, srv, `{
		"source":"e2e.fixture",
		"dataset":"e2e.fixture.evidence_probe",
		"key":"600519.SH/2024Q1",
		"as_of":"2024-09-30T00:00:00Z",
		"payload":{"instrument":"600519.SH","token":"super-secret","nested":{"api_key":"k-1"}}
	}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var written evidence.Record
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &written))
	require.Len(t, written.ContentHash, 64)
	assert.NotContains(t, string(written.Payload), "super-secret")
	assert.NotContains(t, string(written.Payload), "k-1")

	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evidence/"+written.ContentHash, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got evidence.Record
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, written.ContentHash, got.ContentHash)
	assert.NotContains(t, string(got.Payload), "super-secret")
	assert.Contains(t, string(got.Payload), evidence.RedactedPlaceholder)
}

func TestIngestRawRejectsHashMismatch(t *testing.T) {
	srv := newEvidenceServer(newFakeEvidence())
	rec := postRaw(t, srv, `{
		"source":"e2e.fixture","dataset":"d","key":"k",
		"payload":{"a":1},
		"content_hash":"`+strings.Repeat("a", 64)+`"
	}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestListEvidenceRejectsLimitAboveCap(t *testing.T) {
	srv := newEvidenceServer(newFakeEvidence())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evidence?limit=101", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evidence", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body evidenceListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, defaultEvidenceLimit, body.Limit)
	assert.NotNil(t, body.Items)
}

func TestEvidenceUnwiredFailsClosed(t *testing.T) {
	srv := New(nil, WithAuthorizer(allowAllAuthorizer{}))
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/evidence/"+strings.Repeat("0", 64), nil),
		httptest.NewRequest(http.MethodGet, "/api/evidence", nil),
	} {
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, req)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, req.URL.Path)
		var body errorBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "dependency_unavailable", body.Error.Code)
	}
}
