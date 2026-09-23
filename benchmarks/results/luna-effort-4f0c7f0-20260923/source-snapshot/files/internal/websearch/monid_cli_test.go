//go:build !windows

package websearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeMonidFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "monid-fixture")
	script := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMonidCLIClientRequiresVerifiedFreePriceAndZeroBilledRun(t *testing.T) {
	fixture := writeMonidFixture(t, `
case "$1" in
  keys) printf '[{"label":"main","active":true,"key":"masked"}]' ;;
  inspect) printf '{"price":{"type":"PER_CALL","amount":{"value":0,"currency":"USD"}}}' ;;
  run)
    case " $* " in
      *" /search "*) printf '{"status":"COMPLETED","output":{"query":"fixture","results":[{"position":1,"site_name":"site","title":"result","snippet":"snippet","url":"https://example.com"}]},"price":{"type":"PER_CALL","amount":{"value":0,"currency":"USD"}},"billing":{"reportedCost":{"value":0,"currency":"USD","unit":"MICRO_DOLLAR"}},"billedUnits":0}' ;;
      *" /fetch "*) printf '{"status":"COMPLETED","output":{"results":[{"url":"https://example.com","title":"page","text":"body"}],"errors":[]},"price":{"type":"PER_CALL","amount":{"value":0,"currency":"USD"}},"billing":{"reportedCost":{"value":0,"currency":"USD","unit":"MICRO_DOLLAR"}},"billedUnits":0}' ;;
      *) exit 9 ;;
    esac ;;
  *) exit 8 ;;
esac`)
	client, err := NewMonidCLIClient(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if active, err := client.HasActiveKey(context.Background()); err != nil || !active {
		t.Fatalf("HasActiveKey() = %v, %v", active, err)
	}
	results, err := client.Search(context.Background(), "question")
	if err != nil || len(results) != 1 || results[0].Title != "result" {
		t.Fatalf("Search() = %#v, %v", results, err)
	}
	fetched, err := client.Fetch(context.Background(), []string{"https://example.com"})
	if err != nil || len(fetched.Results) != 1 || fetched.Results[0].Text != "body" {
		t.Fatalf("Fetch() = %#v, %v", fetched, err)
	}

	paidFixture := writeMonidFixture(t, `
case "$1" in
  inspect) printf '{"price":{"type":"PER_CALL","amount":{"value":0.01,"currency":"USD"}}}' ;;
  *) echo unexpected-invocation >&2; exit 8 ;;
esac`)
	paid, _ := NewMonidCLIClient(paidFixture)
	if _, err := paid.Search(context.Background(), "no paid fallback"); err == nil || !strings.Contains(err.Error(), "zero-cost") {
		t.Fatalf("Search with paid inspect metadata returned error %v", err)
	}
}

func TestMonidCLIClientDiscardsRunWhenCostIsNotZero(t *testing.T) {
	fixture := writeMonidFixture(t, `
case "$1" in
  inspect) printf '{"price":{"type":"PER_CALL","amount":{"value":0,"currency":"USD"}}}' ;;
  run) printf '{"status":"COMPLETED","output":{"query":"x","results":[]},"price":{"type":"PER_CALL","amount":{"value":0,"currency":"USD"}},"billing":{"reportedCost":{"value":1,"currency":"USD","unit":"MICRO_DOLLAR"}},"billedUnits":1}' ;;
  *) exit 8 ;;
esac`)
	client, _ := NewMonidCLIClient(fixture)
	if _, err := client.Search(context.Background(), "query"); err == nil || !strings.Contains(err.Error(), "did not report zero cost") {
		t.Fatalf("Search with nonzero reported cost returned error %v", err)
	}
}

func TestMonidCLIClientRequiresExplicitPriceAndCostFields(t *testing.T) {
	fixture := writeMonidFixture(t, `
case "$1" in
  inspect) printf '{"price":{"type":"PER_CALL","amount":{"currency":"USD"}}}' ;;
  *) echo unexpected-invocation >&2; exit 8 ;;
esac`)
	client, _ := NewMonidCLIClient(fixture)
	if _, err := client.Search(context.Background(), "query"); err == nil || !strings.Contains(err.Error(), "zero-cost") {
		t.Fatalf("Search with missing inspect price returned error %v", err)
	}

	runFixture := writeMonidFixture(t, `
case "$1" in
  inspect) printf '{"price":{"type":"PER_CALL","amount":{"value":0,"currency":"USD"}}}' ;;
  run) printf '{"status":"COMPLETED","output":{"query":"x","results":[]},"price":{"type":"PER_CALL","amount":{"value":0,"currency":"USD"}},"billing":{"reportedCost":{"currency":"USD","unit":"MICRO_DOLLAR"}},"billedUnits":0}' ;;
  *) exit 8 ;;
esac`)
	runClient, _ := NewMonidCLIClient(runFixture)
	if _, err := runClient.Search(context.Background(), "query"); err == nil || !strings.Contains(err.Error(), "did not report zero cost") {
		t.Fatalf("Search with missing run cost returned error %v", err)
	}
}

func TestMonidCLIClientHonorsCancellation(t *testing.T) {
	fixture := writeMonidFixture(t, `
case "$1" in
  inspect) printf '{"price":{"type":"PER_CALL","amount":{"value":0,"currency":"USD"}}}' ;;
  run) exec sleep 30 ;;
  *) exit 8 ;;
esac`)
	client, _ := NewMonidCLIClient(fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.Search(ctx, "query")
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("canceled Search() err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestMonidCLIClientRejectsUnboundedOutput(t *testing.T) {
	fixture := writeMonidFixture(t, `
case "$1" in
  inspect) head -c 8388609 /dev/zero ;;
  *) exit 8 ;;
esac`)
	client, _ := NewMonidCLIClient(fixture)
	if _, err := client.Search(context.Background(), "query"); err == nil || !strings.Contains(err.Error(), "zero-cost") {
		t.Fatalf("Search with oversized inspect output returned error %v", err)
	}
}
