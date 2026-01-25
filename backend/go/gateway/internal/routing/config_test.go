package routing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolverResolveCluster(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.json")
	data := `{
  "default_cluster": "cluster-a",
  "clusters": {
    "cluster-a": {"brokers": ["localhost:9092"]},
    "cluster-b": {"brokers": ["localhost:9093"]}
  },
  "routes": [
    {"tenant_id": "tenant-1", "warehouse_id": "wh-1", "cluster": "cluster-b"}
  ]
}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write routes file: %v", err)
	}
	resolver, err := Load(path)
	if err != nil {
		t.Fatalf("load routes: %v", err)
	}
	if got, ok := resolver.ResolveCluster("tenant-1", "wh-1"); !ok || got != "cluster-b" {
		t.Fatalf("expected cluster-b, got %q (ok=%v)", got, ok)
	}
	if got, ok := resolver.ResolveCluster("tenant-2", "wh-2"); !ok || got != "cluster-a" {
		t.Fatalf("expected default cluster-a, got %q (ok=%v)", got, ok)
	}
}
