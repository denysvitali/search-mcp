package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadPDFSummaryWithoutPagesOrQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(testPDF("summary fixture content"))
	}))
	defer server.Close()

	out, err := ReadPDF(context.Background(), server.URL, "", "", 2, 20)
	if err != nil {
		t.Fatalf("ReadPDF summary: %v", err)
	}
	for _, want := range []string{"# PDF:", "- Pages: 1", "pages (e.g. \"1-3,17\") or query"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "summary fixture content") {
		t.Errorf("summary should not include page text:\n%s", out)
	}
}

func TestPDFSummaryRejectsGarbage(t *testing.T) {
	if _, err := pdfSummary([]byte("not a pdf"), "https://example.test/x.pdf"); err == nil {
		t.Error("expected error for non-PDF body")
	}
}
