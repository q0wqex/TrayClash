package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var sharedHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     60 * time.Second,
	},
}

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

type MihomoAPI struct {
	BaseURL string
	Client  *http.Client
}

type ProxyGroup struct {
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

func NewMihomoAPI(port string) *MihomoAPI {
	if port == "" {
		port = "9090"
	}
	return &MihomoAPI{
		BaseURL: "http://127.0.0.1:" + port,
		Client:  sharedHTTPClient,
	}
}

func (api *MihomoAPI) GetProxyGroups() (map[string]ProxyGroup, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api.BaseURL+"/proxies", nil)
	if err != nil {
		return nil, err
	}

	resp, err := api.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	if _, err := io.Copy(buf, resp.Body); err != nil {
		return nil, err
	}

	var raw struct {
		Proxies map[string]json.RawMessage `json:"proxies"`
	}
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		return nil, err
	}

	groups := make(map[string]ProxyGroup, len(raw.Proxies)/2)
	for name, data := range raw.Proxies {
		if !bytes.Contains(data, []byte(`"Selector"`)) {
			continue
		}
		var g ProxyGroup
		if err := json.Unmarshal(data, &g); err == nil && g.Type == "Selector" {
			groups[name] = g
		}
	}

	return groups, nil
}

func (api *MihomoAPI) SelectProxy(group, name string) error {
	endpoint := fmt.Sprintf("%s/proxies/%s", api.BaseURL, url.PathEscape(group))
	
	body, _ := json.Marshal(map[string]string{"name": name})
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	
	resp, err := api.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error: %s", resp.Status)
	}
	return nil
}
