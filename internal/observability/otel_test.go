package observability

import (
	"bytes"
	"context"
	"testing"
)

func TestSetupDisabledIsNoop(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{Enabled: false})
	if err != nil {
		t.Fatalf("Setup(disabled) error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("Setup(disabled) returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
}

func TestSetupStdout(t *testing.T) {
	var buf bytes.Buffer
	shutdown, err := Setup(context.Background(), Config{
		Enabled:     true,
		ServiceName: "test",
		Exporter:    "stdout",
		Writer:      &buf,
	})
	if err != nil {
		t.Fatalf("Setup(stdout) error = %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
}

func TestSetupUnknownExporter(t *testing.T) {
	_, err := Setup(context.Background(), Config{Enabled: true, Exporter: "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown exporter")
	}
}

func TestExporterConstructorsUnknown(t *testing.T) {
	if _, err := newTraceExporter(context.Background(), Config{Exporter: "nope"}); err == nil {
		t.Error("expected trace exporter error for unknown exporter")
	}
	if _, err := newMetricExporter(context.Background(), Config{Exporter: "nope"}); err == nil {
		t.Error("expected metric exporter error for unknown exporter")
	}
}
