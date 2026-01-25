package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Cluster struct {
	Brokers  []string `json:"brokers"`
	ClientID string   `json:"client_id"`
}

type Route struct {
	TenantID    string `json:"tenant_id"`
	WarehouseID string `json:"warehouse_id"`
	Cluster     string `json:"cluster"`
}

type Config struct {
	DefaultCluster string             `json:"default_cluster"`
	DefaultTopic   string             `json:"default_topic"`
	TopicMap       map[string]string  `json:"topic_map"`
	Clusters       map[string]Cluster `json:"clusters"`
	Routes         []Route            `json:"routes"`
}

type Resolver struct {
	Config     Config
	routeIndex map[string]string
}

func Load(path string) (Resolver, error) {
	if strings.TrimSpace(path) == "" {
		return Resolver{}, errors.New("routes config path is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Resolver{}, fmt.Errorf("read routes config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Resolver{}, fmt.Errorf("parse routes config: %w", err)
	}
	if len(cfg.Clusters) == 0 {
		return Resolver{}, errors.New("routes config must define clusters")
	}
	for name, cluster := range cfg.Clusters {
		if len(cluster.Brokers) == 0 {
			return Resolver{}, fmt.Errorf("cluster %q must define brokers", name)
		}
	}
	index := make(map[string]string, len(cfg.Routes))
	for _, route := range cfg.Routes {
		key := routeKey(route.TenantID, route.WarehouseID)
		if key == "" {
			return Resolver{}, errors.New("route must include tenant_id and warehouse_id")
		}
		if _, ok := cfg.Clusters[route.Cluster]; !ok {
			return Resolver{}, fmt.Errorf("route references unknown cluster %q", route.Cluster)
		}
		if _, exists := index[key]; exists {
			return Resolver{}, fmt.Errorf("duplicate route for tenant_id=%q warehouse_id=%q", route.TenantID, route.WarehouseID)
		}
		index[key] = route.Cluster
	}
	if cfg.DefaultCluster != "" {
		if _, ok := cfg.Clusters[cfg.DefaultCluster]; !ok {
			return Resolver{}, fmt.Errorf("default_cluster %q not found in clusters", cfg.DefaultCluster)
		}
	}
	return Resolver{Config: cfg, routeIndex: index}, nil
}

func (r Resolver) ResolveCluster(tenantID string, warehouseID string) (string, bool) {
	if r.routeIndex == nil {
		return "", false
	}
	if v, ok := r.routeIndex[routeKey(tenantID, warehouseID)]; ok {
		return v, true
	}
	if r.Config.DefaultCluster != "" {
		return r.Config.DefaultCluster, true
	}
	return "", false
}

func (r Resolver) ResolveTopic(eventType string, requestedTopic string) string {
	if strings.TrimSpace(requestedTopic) != "" {
		return strings.TrimSpace(requestedTopic)
	}
	if r.Config.TopicMap != nil {
		if v, ok := r.Config.TopicMap[strings.TrimSpace(eventType)]; ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if strings.TrimSpace(r.Config.DefaultTopic) != "" {
		return strings.TrimSpace(r.Config.DefaultTopic)
	}
	return strings.TrimSpace(eventType)
}

func routeKey(tenantID string, warehouseID string) string {
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	warehouseID = strings.ToLower(strings.TrimSpace(warehouseID))
	if tenantID == "" || warehouseID == "" {
		return ""
	}
	return tenantID + "|" + warehouseID
}

func DefaultRoutesPath(env string) (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(env) == "" {
		env = "dev"
	}
	return filepath.Join(root, "backend", "configs", env+".gateway.routes.json"), nil
}

func findRepoRoot() (string, error) {
	start, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := start
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "backend", "configs")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("repo root not found")
}
